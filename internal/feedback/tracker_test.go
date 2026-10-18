package feedback

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"codereviewagent/internal/db"
	"codereviewagent/internal/github"
	"codereviewagent/internal/repository"
)

// mockGitHub implements GitHubClient with hardcoded file contents
// (CLAUDE.v.1.4.md: never call the real GitHub API in tests).
type mockGitHub struct {
	mu    sync.Mutex
	files map[string]string // "ref|path" -> content
	calls []string          // "ref|path" in call order
}

func (m *mockGitHub) GetFileAtRef(_ context.Context, _, _, path, ref string) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, ref+"|"+path)
	m.mu.Unlock()
	if c, ok := m.files[ref+"|"+path]; ok {
		return c, nil
	}
	return "", fmt.Errorf("%w: %s@%s", github.ErrFileNotFound, path, ref)
}

// oldLoginFile is the review-time version of the tracked file.
const oldLoginFile = `package auth

import "database/sql"

func Login(db *sql.DB, user string, pass string) error {
	rows, err := db.Query("SELECT * FROM users WHERE name = '" + user + "'")
	if err != nil {
		return err
	}
	defer rows.Close()
	return nil
}
`

// rewrittenLoginFile rewrites the login flow substantially: similarity to
// the old version must fall below the 0.8 threshold.
const rewrittenLoginFile = `package auth

import (
	"context"
	"database/sql"
)

func Login(ctx context.Context, db *sql.DB, user string, pass string) error {
	var hash string
	if err := db.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE name = ?", user).Scan(&hash); err != nil {
		return err
	}
	return checkPassword(hash, pass)
}
`

// indentedLoginFile only re-indents the code: similarity must stay above
// the threshold.
const indentedLoginFile = "package auth\n\nimport \"database/sql\"\n\nfunc Login(db *sql.DB, user string, pass string) error {\n" +
	"        rows, err := db.Query(\"SELECT * FROM users WHERE name = '\" + user + \"'\")\n" +
	"        if err != nil {\n" +
	"                return err\n" +
	"        }\n" +
	"        defer rows.Close()\n" +
	"        return nil\n" +
	"}\n"

// feedbackTestDatabaseURL uses a dedicated database so parallel test
// packages cannot truncate each other's fixtures.
const feedbackTestDatabaseURL = "postgres://postgres:postgres123@localhost:5432/codereviewagent_feedback?sslmode=disable"

func newFeedbackPool(t *testing.T) (*pgxpool.Pool, *repository.FeedbackRepo) {
	t.Helper()
	if os.Getenv("SKIP_DB_TESTS") == "1" {
		t.Skip("SKIP_DB_TESTS is set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.EnsureDatabase(ctx, feedbackTestDatabaseURL); err != nil {
		t.Skipf("prepare test database: %v", err)
	}
	pool, err := db.Connect(ctx, feedbackTestDatabaseURL)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE pr_commits, findings, reviews RESTART IDENTITY CASCADE"); err != nil {
		pool.Close()
		t.Fatalf("truncate: %v", err)
	}
	return pool, repository.NewFeedbackRepo(pool)
}

// feedbackFixture seeds one completed review with one pending finding for
// the given repo/PR.
func feedbackFixture(t *testing.T, reviewRepo *repository.ReviewRepo, _ *repository.FeedbackRepo, repoFullName string, prNumber int, file string, line int) int64 {
	t.Helper()
	reviewID, err := reviewRepo.CreateReview(context.Background(), &repository.Review{
		RepoFullName: repoFullName, PRNumber: prNumber, HeadSHA: "review-sha", Status: "completed",
	})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	id, err := reviewRepo.CreateFinding(context.Background(), &repository.Finding{
		ReviewID: reviewID, Agent: "security", Severity: "critical",
		FilePath: file, LineNumber: line, Message: "SQL injection", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("CreateFinding: %v", err)
	}
	return id
}

func statusOf(t *testing.T, pool *pgxpool.Pool, findingID int64) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(),
		"SELECT feedback_status FROM findings WHERE id = $1", findingID).Scan(&status); err != nil {
		t.Fatalf("read feedback status: %v", err)
	}
	return status
}

// TestTrackerSynchronizeModified covers AC-84: a commit that rewrites the
// tracked code moves the finding from pending to accepted_implicit.
func TestTrackerSynchronizeModified(t *testing.T) {
	pool, fbRepo := newFeedbackPool(t)
	defer pool.Close()
	reviewRepo := repository.NewReviewRepo(pool)

	const repoName = "fb/track"
	const pr = 21
	findingID := feedbackFixture(t, reviewRepo, fbRepo, repoName, pr, "auth/login.go", 6)

	gh := &mockGitHub{files: map[string]string{
		"oldsha|auth/login.go": oldLoginFile,
		"newsha|auth/login.go": rewrittenLoginFile,
	}}
	tracker := NewTracker(fbRepo, gh, DefaultSimilarityThreshold, zap.NewNop())

	if err := tracker.TrackOnSynchronize(context.Background(), repoName, pr, "newsha", "oldsha"); err != nil {
		t.Fatalf("TrackOnSynchronize: %v", err)
	}
	if got := statusOf(t, pool, findingID); got != string(StatusAcceptedImplicit) {
		t.Errorf("status = %q, want accepted_implicit", got)
	}

	// Commit history recorded (FR-39).
	commits, err := fbRepo.ListPRCommits(context.Background(), repoName, pr)
	if err != nil {
		t.Fatalf("ListPRCommits: %v", err)
	}
	if len(commits) != 2 {
		t.Errorf("pr_commits rows = %d, want 2 (before+after)", len(commits))
	}

	// Idempotent: a repeated run must not change the resolved finding.
	if err := tracker.TrackOnSynchronize(context.Background(), repoName, pr, "newsha", "oldsha"); err != nil {
		t.Fatalf("TrackOnSynchronize again: %v", err)
	}
	if got := statusOf(t, pool, findingID); got != string(StatusAcceptedImplicit) {
		t.Errorf("status after re-run = %q, want unchanged accepted_implicit", got)
	}
}

// TestTrackerSynchronizeUnchangedStaysPending verifies that an
// indentation-only push keeps the finding pending.
func TestTrackerSynchronizeUnchangedStaysPending(t *testing.T) {
	pool, fbRepo := newFeedbackPool(t)
	defer pool.Close()
	reviewRepo := repository.NewReviewRepo(pool)

	repoName, pr := "fb/unchanged", 22
	findingID := feedbackFixture(t, reviewRepo, fbRepo, repoName, pr, "auth/login.go", 6)

	gh := &mockGitHub{files: map[string]string{
		"oldsha|auth/login.go": oldLoginFile,
		"newsha|auth/login.go": indentedLoginFile,
	}}
	tracker := NewTracker(fbRepo, gh, DefaultSimilarityThreshold, zap.NewNop())

	if err := tracker.TrackOnSynchronize(context.Background(), repoName, pr, "newsha", "oldsha"); err != nil {
		t.Fatalf("TrackOnSynchronize: %v", err)
	}
	if got := statusOf(t, pool, findingID); got != string(StatusPending) {
		t.Errorf("status = %q, want pending (code unchanged)", got)
	}
}

// TestTrackerSynchronizeFileDeleted covers AC-86 / spec 15.4: a push that
// deletes the file resolves the finding as accepted_implicit.
func TestTrackerSynchronizeFileDeleted(t *testing.T) {
	pool, fbRepo := newFeedbackPool(t)
	defer pool.Close()
	reviewRepo := repository.NewReviewRepo(pool)

	repoName, pr := "fb/deleted", 23
	findingID := feedbackFixture(t, reviewRepo, fbRepo, repoName, pr, "auth/login.go", 6)

	gh := &mockGitHub{files: map[string]string{
		"oldsha|auth/login.go": oldLoginFile,
		// newsha: no entry -> not found
	}}
	tracker := NewTracker(fbRepo, gh, DefaultSimilarityThreshold, zap.NewNop())

	if err := tracker.TrackOnSynchronize(context.Background(), repoName, pr, "newsha", "oldsha"); err != nil {
		t.Fatalf("TrackOnSynchronize: %v", err)
	}
	if got := statusOf(t, pool, findingID); got != string(StatusAcceptedImplicit) {
		t.Errorf("status = %q, want accepted_implicit (file deleted)", got)
	}
}

// TestTrackerClosedResolvesPending covers AC-85: closing (merged or not)
// resolves the remaining pending findings as ignored_implicit.
func TestTrackerClosedResolvesPending(t *testing.T) {
	pool, fbRepo := newFeedbackPool(t)
	defer pool.Close()
	reviewRepo := repository.NewReviewRepo(pool)

	repoName, pr := "fb/closed", 24
	f1 := feedbackFixture(t, reviewRepo, fbRepo, repoName, pr, "auth/login.go", 6)
	f2 := feedbackFixture(t, reviewRepo, fbRepo, repoName, pr, "auth/token.go", 3)

	gh := &mockGitHub{files: map[string]string{
		"oldsha|auth/login.go": oldLoginFile,
		"newsha|auth/login.go": indentedLoginFile,
	}}
	tracker := NewTracker(fbRepo, gh, DefaultSimilarityThreshold, zap.NewNop())

	// A first synchronize leaves the unchanged finding pending.
	if err := tracker.TrackOnSynchronize(context.Background(), repoName, pr, "newsha", "oldsha"); err != nil {
		t.Fatalf("TrackOnSynchronize: %v", err)
	}
	if got := statusOf(t, pool, f1); got != string(StatusPending) {
		t.Fatalf("pre-close status = %q, want pending", got)
	}

	if err := tracker.TrackOnClosed(context.Background(), repoName, pr, true); err != nil {
		t.Fatalf("TrackOnClosed: %v", err)
	}
	if got := statusOf(t, pool, f1); got != string(StatusIgnoredImplicit) {
		t.Errorf("status = %q, want ignored_implicit after merge", got)
	}
	if got := statusOf(t, pool, f2); got != string(StatusIgnoredImplicit) {
		t.Errorf("untouched finding status = %q, want ignored_implicit", got)
	}

	// Idempotent re-run + directly-closed PRs behave the same (spec 15.4).
	if err := tracker.TrackOnClosed(context.Background(), repoName, pr, false); err != nil {
		t.Fatalf("TrackOnClosed again: %v", err)
	}
	if got := statusOf(t, pool, f1); got != string(StatusIgnoredImplicit) {
		t.Errorf("status after re-run = %q, want unchanged", got)
	}
}

// TestWindowLines covers the local-context extraction (FR-42).
func TestWindowLines(t *testing.T) {
	content := strings.Repeat("line\n", 20) // 20 lines
	got := windowLines(content, 10, 5)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 11 {
		t.Errorf("window size = %d, want 11 (finding + 5 on each side)", len(lines))
	}

	if got := windowLines(content, 2, 5); len(strings.Split(strings.TrimSuffix(got, "\n"), "\n")) != 7 {
		t.Errorf("window near file start must clamp to the file bounds")
	}
}
