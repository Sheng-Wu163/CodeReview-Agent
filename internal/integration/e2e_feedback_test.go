// e2e_feedback_test.go implements the v1.4 T43 deliverable: the full
// implicit-feedback lifecycle with everything mocked - a review produces
// pending findings, a synchronize webhook rewrites the code, a closed
// webhook merges the PR, and the metrics API reflects the adoption rates.
// No real GitHub call and no LLM is involved (CLAUDE.v.1.4.md).
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"codereviewagent/internal/db"
	"codereviewagent/internal/feedback"
	"codereviewagent/internal/github"
	"codereviewagent/internal/handler"
	"codereviewagent/internal/middleware"
	"codereviewagent/internal/repository"
)

// feedbackTestDatabaseURL gives this package its own database so parallel
// test packages cannot truncate each other's fixtures.
const feedbackTestDatabaseURL = "postgres://postgres:postgres123@localhost:5432/codereviewagent_integration?sslmode=disable"

// feedbackGitHub serves hardcoded file contents for the lifecycle test.
type feedbackGitHub struct {
	files map[string]string // "ref|path" -> content
}

// e2eLoginFileOld is the review-time version of the tracked file.
const e2eLoginFileOld = `package auth

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

// e2eLoginFileNew rewrites the login flow substantially (similarity < 0.8).
const e2eLoginFileNew = `package auth

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

func (m *feedbackGitHub) GetFileAtRef(_ context.Context, _, _, path, ref string) (string, error) {
	if c, ok := m.files[ref+"|"+path]; ok {
		return c, nil
	}
	return "", fmt.Errorf("%w: %s@%s", github.ErrFileNotFound, path, ref)
}

// waitForStatus polls the finding's feedback status until it equals want.
func waitForStatus(t *testing.T, pool *pgxpool.Pool, findingID int64, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var got string
		if err := pool.QueryRow(context.Background(),
			"SELECT feedback_status FROM findings WHERE id = $1", findingID).Scan(&got); err != nil {
			t.Fatalf("read feedback status: %v", err)
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("finding %d status = %q after %v, want %q", findingID, got, timeout, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestE2EFeedbackLifecycle(t *testing.T) {
	if os.Getenv("SKIP_DB_TESTS") == "1" {
		t.Skip("SKIP_DB_TESTS is set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.EnsureDatabase(ctx, feedbackTestDatabaseURL); err != nil {
		t.Skipf("prepare test database: %v", err)
	}
	pool, err := db.Connect(ctx, feedbackTestDatabaseURL)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE pr_commits, findings, reviews RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	logger := zap.NewNop()
	reviewRepo := repository.NewReviewRepo(pool)
	feedbackRepo := repository.NewFeedbackRepo(pool)

	// Stage 1: a completed review with two pending findings on the PR.
	reviewID, err := reviewRepo.CreateReview(ctx, &repository.Review{
		RepoFullName: "fb/e2e", PRNumber: 77, HeadSHA: "review-sha", Status: "completed",
	})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	// f1 will be fixed by the synchronize push; f2 stays untouched.
	f1, err := reviewRepo.CreateFinding(ctx, &repository.Finding{
		ReviewID: reviewID, Agent: "security", Severity: "critical",
		FilePath: "auth/login.go", LineNumber: 6, Message: "SQL injection", Confidence: 0.95,
	})
	if err != nil {
		t.Fatalf("CreateFinding f1: %v", err)
	}
	f2, err := reviewRepo.CreateFinding(ctx, &repository.Finding{
		ReviewID: reviewID, Agent: "logic", Severity: "warning",
		FilePath: "auth/session.go", LineNumber: 3, Message: "unhandled error", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("CreateFinding f2: %v", err)
	}

	// Stage 2: wire the full HTTP surface (webhook + dashboard) with a
	// mocked GitHub file source.
	gh := &feedbackGitHub{files: map[string]string{
		"oldsha|auth/login.go": e2eLoginFileOld,
		"newsha|auth/login.go": e2eLoginFileNew,
	}}
	tracker := feedback.NewTracker(feedbackRepo, gh, feedback.DefaultSimilarityThreshold, logger)

	const secret = "e2e-secret"
	r := chi.NewRouter()
	r.Post("/webhook", handler.NewWebhook(logger, nil, tracker).ServeHTTP)
	dashboard, err := handler.NewDashboard(reviewRepo, logger, "../../templates")
	if err != nil {
		t.Fatalf("NewDashboard: %v", err)
	}
	dashboard.SetFeedbackRepo(feedbackRepo)
	dashboard.Register(r)
	server := httptest.NewServer(r)
	defer server.Close()

	// post sends an HMAC-signed webhook and asserts a 200.
	post := func(body string) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/webhook", strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", middleware.ComputeSignature(secret, []byte(body)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /webhook: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("webhook status = %d, want 200", resp.StatusCode)
		}
	}

	// Stage 3: a synchronize push rewrites auth/login.go -> f1 becomes
	// accepted_implicit (AC-84). auth/session.go is never fetched -> f2
	// stays pending.
	post(`{"action":"synchronize","number":77,"before":"oldsha","after":"newsha","pull_request":{"head":{"sha":"newsha"}},"repository":{"full_name":"fb/e2e"}}`)
	waitForStatus(t, pool, f1, string(feedback.StatusAcceptedImplicit), 5*time.Second)
	var f2Status string
	if err := pool.QueryRow(ctx, "SELECT feedback_status FROM findings WHERE id = $1", f2).Scan(&f2Status); err != nil {
		t.Fatalf("read f2 status: %v", err)
	}
	if f2Status != string(feedback.StatusPending) {
		t.Errorf("f2 status = %q, want pending", f2Status)
	}

	// Stage 4: the PR merges -> the untouched f2 becomes ignored_implicit
	// (AC-85). Repeated events must not change anything (idempotency).
	post(`{"action":"closed","number":77,"pull_request":{"merged":true,"head":{"sha":"newsha"}},"repository":{"full_name":"fb/e2e"}}`)
	waitForStatus(t, pool, f2, string(feedback.StatusIgnoredImplicit), 5*time.Second)
	post(`{"action":"closed","number":77,"pull_request":{"merged":true,"head":{"sha":"newsha"}},"repository":{"full_name":"fb/e2e"}}`)
	time.Sleep(200 * time.Millisecond)
	var f1Status string
	if err := pool.QueryRow(ctx, "SELECT feedback_status FROM findings WHERE id = $1", f1).Scan(&f1Status); err != nil {
		t.Fatalf("read f1 status: %v", err)
	}
	if f1Status != string(feedback.StatusAcceptedImplicit) {
		t.Errorf("f1 status = %q, want unchanged accepted_implicit (idempotency)", f1Status)
	}

	// Stage 5: the metrics API reports the adoption rates (AC-87). security:
	// 1/1 resolved accepted; logic: 1/1 ignored.
	resp, err := http.Get(server.URL + "/api/metrics/feedback")
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Agents []struct {
			Agent      string  `json:"agent"`
			Total      int     `json:"total"`
			AcceptRate float64 `json:"accept_rate"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	rates := map[string]float64{}
	for _, a := range body.Agents {
		rates[a.Agent] = a.AcceptRate
	}
	if rates["security"] != 1.0 {
		t.Errorf("security accept rate = %v, want 1.0", rates["security"])
	}
	if rates["logic"] != 0.0 {
		t.Errorf("logic accept rate = %v, want 0.0 (ignored)", rates["logic"])
	}

	// Stage 6: the commit history recorded both synchronize SHAs (FR-39).
	commits, err := feedbackRepo.ListPRCommits(ctx, "fb/e2e", 77)
	if err != nil {
		t.Fatalf("ListPRCommits: %v", err)
	}
	if len(commits) != 2 {
		t.Errorf("pr_commits rows = %d, want 2", len(commits))
	}
}
