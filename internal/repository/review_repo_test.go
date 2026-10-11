package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"codereviewagent/internal/db"
)

// testDatabaseURL points at the local PostgreSQL instance used for
// integration tests. Tests are skipped when it is unreachable.
const testDatabaseURL = "postgres://postgres:postgres123@localhost:5432/codereviewagent?sslmode=disable"

// newTestRepo connects to the test database, migrates it and returns a repo
// plus a cleanup function.
func newTestRepo(t *testing.T) (*ReviewRepo, func()) {
	t.Helper()

	if os.Getenv("SKIP_DB_TESTS") == "1" {
		t.Skip("SKIP_DB_TESTS is set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.Connect(ctx, testDatabaseURL)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate test database: %v", err)
	}
	return NewReviewRepo(pool), pool.Close
}

// truncateReviews clears all review/finding rows between test cases.
func truncateReviews(t *testing.T, repo *ReviewRepo) {
	t.Helper()
	_, err := repo.pool.Exec(context.Background(), "TRUNCATE findings, reviews RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
}

func TestCreateAndGetReview(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()
	truncateReviews(t, repo)

	ctx := context.Background()
	want := &Review{
		RepoFullName: "foo/bar",
		PRNumber:     42,
		HeadSHA:      "abc123",
		Status:       "pending",
		Summary:      "review in progress",
	}

	id, err := repo.CreateReview(ctx, want)
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	if id <= 0 {
		t.Errorf("CreateReview id = %d, want > 0", id)
	}

	got, err := repo.GetReview(ctx, id)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if got.RepoFullName != want.RepoFullName {
		t.Errorf("RepoFullName = %q, want %q", got.RepoFullName, want.RepoFullName)
	}
	if got.PRNumber != want.PRNumber {
		t.Errorf("PRNumber = %d, want %d", got.PRNumber, want.PRNumber)
	}
	if got.HeadSHA != want.HeadSHA {
		t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, want.HeadSHA)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.Summary != want.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, want.Summary)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set by the database")
	}
}

func TestGetReviewNotFound(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()
	truncateReviews(t, repo)

	_, err := repo.GetReview(context.Background(), 999999)
	if err != pgx.ErrNoRows {
		t.Errorf("GetReview error = %v, want pgx.ErrNoRows", err)
	}
}

func TestListReviews(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()
	truncateReviews(t, repo)

	ctx := context.Background()
	// Insert in a known order; ListReviews must return newest first.
	for i := 1; i <= 3; i++ {
		_, err := repo.CreateReview(ctx, &Review{
			RepoFullName: fmt.Sprintf("owner/repo-%d", i),
			PRNumber:     i,
			HeadSHA:      fmt.Sprintf("sha-%d", i),
			Status:       "pending",
		})
		if err != nil {
			t.Fatalf("CreateReview %d: %v", i, err)
		}
		// Ensure distinct created_at ordering.
		time.Sleep(10 * time.Millisecond)
	}

	reviews, err := repo.ListReviews(ctx, 10)
	if err != nil {
		t.Fatalf("ListReviews: %v", err)
	}
	if len(reviews) != 3 {
		t.Fatalf("ListReviews len = %d, want 3", len(reviews))
	}
	for i, rev := range reviews {
		wantPR := 3 - i
		if rev.PRNumber != wantPR {
			t.Errorf("reviews[%d].PRNumber = %d, want %d (newest first)", i, rev.PRNumber, wantPR)
		}
	}
}

func TestListReviewsLimit(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()
	truncateReviews(t, repo)

	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		_, err := repo.CreateReview(ctx, &Review{
			RepoFullName: "owner/repo",
			PRNumber:     i,
			HeadSHA:      fmt.Sprintf("sha-%d", i),
			Status:       "pending",
		})
		if err != nil {
			t.Fatalf("CreateReview %d: %v", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	reviews, err := repo.ListReviews(ctx, 2)
	if err != nil {
		t.Fatalf("ListReviews: %v", err)
	}
	if len(reviews) != 2 {
		t.Errorf("ListReviews len = %d, want 2", len(reviews))
	}
}

func TestCreateFinding(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()
	truncateReviews(t, repo)

	ctx := context.Background()
	reviewID, err := repo.CreateReview(ctx, &Review{
		RepoFullName: "foo/bar",
		PRNumber:     7,
		HeadSHA:      "deadbeef",
		Status:       "pending",
	})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}

	want := &Finding{
		ReviewID:   reviewID,
		Agent:      "security",
		Severity:   "critical",
		FilePath:   "cmd/server/main.go",
		LineNumber: 10,
		Message:    "SQL injection: string concatenation in query",
		Suggestion: "Use parameterized queries",
		Confidence: 0.95,
	}
	id, err := repo.CreateFinding(ctx, want)
	if err != nil {
		t.Fatalf("CreateFinding: %v", err)
	}
	if id <= 0 {
		t.Errorf("CreateFinding id = %d, want > 0", id)
	}

	// Verify the row was persisted correctly by reading it back with raw SQL.
	var (
		agent, severity, filePath, message string
		lineNumber                         int
		confidence                         float64
	)
	err = repo.pool.QueryRow(ctx, `
		SELECT agent, severity, file_path, line_number, message, confidence
		FROM findings WHERE id = $1`, id,
	).Scan(&agent, &severity, &filePath, &lineNumber, &message, &confidence)
	if err != nil {
		t.Fatalf("read finding back: %v", err)
	}
	if agent != want.Agent || severity != want.Severity ||
		filePath != want.FilePath || lineNumber != want.LineNumber ||
		message != want.Message || confidence != want.Confidence {
		t.Errorf("finding round-trip mismatch: got (%s,%s,%s,%d,%s,%f), want (%s,%s,%s,%d,%s,%f)",
			agent, severity, filePath, lineNumber, message, confidence,
			want.Agent, want.Severity, want.FilePath, want.LineNumber, want.Message, want.Confidence)
	}
}

func TestCreateFindingForeignKeyViolation(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()
	truncateReviews(t, repo)

	_, err := repo.CreateFinding(context.Background(), &Finding{
		ReviewID:   999999,
		Agent:      "security",
		Severity:   "warning",
		FilePath:   "x.go",
		LineNumber: 1,
		Message:    "orphan finding",
	})
	if err == nil {
		t.Error("CreateFinding with non-existent review_id should fail")
	}
}

func TestListFindings(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()
	truncateReviews(t, repo)

	ctx := context.Background()
	reviewID, err := repo.CreateReview(ctx, &Review{
		RepoFullName: "foo/bar", PRNumber: 9, HeadSHA: "sha-9", Status: "completed",
	})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}

	insert := func(sev, agent, msg string) {
		t.Helper()
		if _, err := repo.CreateFinding(ctx, &Finding{
			ReviewID: reviewID, Agent: agent, Severity: sev,
			FilePath: "a.go", LineNumber: 1, Message: msg, Confidence: 0.8,
		}); err != nil {
			t.Fatalf("CreateFinding: %v", err)
		}
	}
	insert("warning", "logic", "unhandled error")
	insert("critical", "security", "SQL injection")
	insert("suggestion", "lint", "naming")
	insert("warning", "performance", "string concat")

	findings, err := repo.ListFindings(ctx, reviewID)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(findings) != 4 {
		t.Fatalf("findings = %d, want 4", len(findings))
	}
	wantOrder := []string{"critical", "warning", "warning", "suggestion"}
	for i, want := range wantOrder {
		if findings[i].Severity != want {
			t.Errorf("findings[%d].Severity = %q, want %q", i, findings[i].Severity, want)
		}
	}
	// Findings of another review must not leak in.
	otherID, err := repo.CreateReview(ctx, &Review{RepoFullName: "x/y", PRNumber: 1, HeadSHA: "s", Status: "pending"})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	other, err := repo.ListFindings(ctx, otherID)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("other review findings = %d, want 0", len(other))
	}
}

func TestUpdateReviewStatus(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()
	truncateReviews(t, repo)

	ctx := context.Background()
	id, err := repo.CreateReview(ctx, &Review{RepoFullName: "a/b", PRNumber: 1, HeadSHA: "s", Status: "pending"})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}

	if err := repo.UpdateReviewStatus(ctx, id, strPtr("completed"), strPtr("2 findings")); err != nil {
		t.Fatalf("UpdateReviewStatus: %v", err)
	}
	got, err := repo.GetReview(ctx, id)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if got.Status != "completed" || got.Summary != "2 findings" {
		t.Errorf("review = %s/%s, want completed/2 findings", got.Status, got.Summary)
	}
	if got.UpdatedAt == nil {
		t.Error("UpdatedAt should be set after update")
	}

	// Summary must survive a nil-summary update.
	if err := repo.UpdateReviewStatus(ctx, id, strPtr("failed"), nil); err != nil {
		t.Fatalf("UpdateReviewStatus: %v", err)
	}
	got, err = repo.GetReview(ctx, id)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if got.Summary != "2 findings" {
		t.Errorf("Summary = %q, want preserved", got.Summary)
	}
}

func strPtr(s string) *string { return &s }
