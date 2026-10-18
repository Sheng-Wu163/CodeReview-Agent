package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"codereviewagent/internal/db"
	"codereviewagent/internal/repository"
)

// dashboardTestDatabaseURL uses a dedicated database so parallel test
// packages cannot truncate each other's fixtures.
const dashboardTestDatabaseURL = "postgres://postgres:postgres123@localhost:5432/codereviewagent_handler?sslmode=disable"

// newDashboardHarness sets up a migrated database, a dashboard handler and
// a chi server, returning the server and a cleanup function.
func newDashboardHarness(t *testing.T) (*httptest.Server, *repository.ReviewRepo, *repository.FeedbackRepo) {
	t.Helper()

	if os.Getenv("SKIP_DB_TESTS") == "1" {
		t.Skip("SKIP_DB_TESTS is set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := db.EnsureDatabase(ctx, dashboardTestDatabaseURL); err != nil {
		t.Skipf("prepare test database: %v", err)
	}
	pool, err := db.Connect(ctx, dashboardTestDatabaseURL)
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
	repo := repository.NewReviewRepo(pool)
	fb := repository.NewFeedbackRepo(pool)

	dashboard, err := NewDashboard(repo, zap.NewNop(), "../../templates")
	if err != nil {
		pool.Close()
		t.Fatalf("NewDashboard: %v", err)
	}
	dashboard.SetFeedbackRepo(fb)
	r := chi.NewRouter()
	dashboard.Register(r)
	server := httptest.NewServer(r)
	t.Cleanup(func() {
		server.Close()
		pool.Close()
	})
	return server, repo, fb
}

// seedReview inserts a review with findings and returns the review ID.
func seedReview(t *testing.T, repo *repository.ReviewRepo, repoFullName string, prNumber int) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := repo.CreateReview(ctx, &repository.Review{
		RepoFullName: repoFullName, PRNumber: prNumber, HeadSHA: "sha-" + repoFullName,
		Status: "completed", Summary: "test review",
	})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	for _, f := range []repository.Finding{
		{ReviewID: id, Agent: "security", Severity: "critical", FilePath: "a.go", LineNumber: 1,
			Message: "SQL injection", Suggestion: "parameterize", Confidence: 0.95},
		{ReviewID: id, Agent: "logic", Severity: "warning", FilePath: "b.go", LineNumber: 2,
			Message: "unhandled error", Confidence: 0.8},
	} {
		if _, err := repo.CreateFinding(ctx, &f); err != nil {
			t.Fatalf("CreateFinding: %v", err)
		}
	}
	return id
}

func TestDashboardAPIListReviews(t *testing.T) {
	server, repo, _ := newDashboardHarness(t)
	id := seedReview(t, repo, "ac18/list", 11)

	resp, err := http.Get(server.URL + "/api/reviews")
	if err != nil {
		t.Fatalf("GET /api/reviews: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Reviews []apiReview `json:"reviews"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found *apiReview
	for i := range body.Reviews {
		if body.Reviews[i].ID == id {
			found = &body.Reviews[i]
		}
	}
	if found == nil {
		t.Fatalf("seeded review %d not in response: %+v", id, body.Reviews)
	}
	if found.RepoFullName != "ac18/list" || found.PRNumber != 11 {
		t.Errorf("review = %+v, want ac18/list #11", found)
	}
	if found.FindingsCount != 2 {
		t.Errorf("FindingsCount = %d, want 2", found.FindingsCount)
	}
}

func TestDashboardAPIDetail(t *testing.T) {
	server, repo, _ := newDashboardHarness(t)
	id := seedReview(t, repo, "ac18/detail", 12)

	resp, err := http.Get(server.URL + fmt.Sprintf("/api/reviews/%d", id))
	if err != nil {
		t.Fatalf("GET /api/reviews/%d: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Review   apiReview    `json:"review"`
		Findings []apiFinding `json:"findings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Review.ID != id || body.Review.RepoFullName != "ac18/detail" {
		t.Errorf("review = %+v", body.Review)
	}
	if len(body.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(body.Findings))
	}
	if body.Findings[0].Severity != "critical" || body.Findings[0].Confidence <= 0 || body.Findings[0].Confidence > 1 {
		t.Errorf("findings[0] = %+v, want critical first with confidence in (0,1]", body.Findings[0])
	}
	if body.Findings[0].Message != "SQL injection" {
		t.Errorf("findings[0].Message = %q, want SQL injection", body.Findings[0].Message)
	}
}

func TestDashboardAPINotFound(t *testing.T) {
	server, _, _ := newDashboardHarness(t)

	resp, err := http.Get(server.URL + "/api/reviews/99999999")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDashboardAPIInvalidID(t *testing.T) {
	server, _, _ := newDashboardHarness(t)

	resp, err := http.Get(server.URL + "/api/reviews/notanumber")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDashboardListPage(t *testing.T) {
	server, repo, _ := newDashboardHarness(t)
	id := seedReview(t, repo, "ac19/page", 13)

	resp, err := http.Get(server.URL + "/dashboard")
	if err != nil {
		t.Fatalf("GET /dashboard: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	page, _ := io.ReadAll(resp.Body)
	html := string(page)
	for _, want := range []string{"ac19/page", "#13", "Review history", "/dashboard/reviews/"} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard page missing %q", want)
		}
	}
	_ = id
}

func TestDashboardDetailPage(t *testing.T) {
	server, repo, _ := newDashboardHarness(t)
	id := seedReview(t, repo, "ac20/page", 14)

	resp, err := http.Get(server.URL + fmt.Sprintf("/dashboard/reviews/%d", id))
	if err != nil {
		t.Fatalf("GET detail: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	page, _ := io.ReadAll(resp.Body)
	html := string(page)
	for _, want := range []string{"ac20/page", "#14", "SQL injection", "unhandled error", "sev-critical", "0.95"} {
		if !strings.Contains(html, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}

func TestDashboardDetailPageNotFound(t *testing.T) {
	server, _, _ := newDashboardHarness(t)

	resp, err := http.Get(server.URL + "/dashboard/reviews/99999999")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestAPIMetricsFeedback covers v1.4 AC-87: the feedback metrics API
// returns per-agent adoption rates computed over resolved findings.
func TestAPIMetricsFeedback(t *testing.T) {
	server, repo, fb := newDashboardHarness(t)
	ctx := context.Background()

	// 1 review with 3 security findings: 2 accepted, 1 pending; and 2 logic
	// findings: 1 accepted, 1 ignored.
	reviewID, err := repo.CreateReview(ctx, &repository.Review{
		RepoFullName: "fb/metrics", PRNumber: 31, HeadSHA: "sha-m", Status: "completed",
	})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	fixtures := []struct {
		agent string
		file  string
		line  int
	}{
		{"security", "a.go", 1}, {"security", "b.go", 2}, {"security", "c.go", 3},
		{"logic", "a.go", 5}, {"logic", "b.go", 6},
	}
	ids := make([]int64, 0, len(fixtures))
	for _, fx := range fixtures {
		id, err := repo.CreateFinding(ctx, &repository.Finding{
			ReviewID: reviewID, Agent: fx.agent, Severity: "warning",
			FilePath: fx.file, LineNumber: fx.line, Message: "m", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("CreateFinding: %v", err)
		}
		ids = append(ids, id)
	}
	_, _ = fb.UpdateFindingFeedbackStatus(ctx, ids[0], repository.FeedbackAcceptedImplicit)
	_, _ = fb.UpdateFindingFeedbackStatus(ctx, ids[1], repository.FeedbackAcceptedImplicit)
	_, _ = fb.UpdateFindingFeedbackStatus(ctx, ids[3], repository.FeedbackAcceptedImplicit)
	_, _ = fb.UpdateFindingFeedbackStatus(ctx, ids[4], repository.FeedbackIgnoredImplicit)

	resp, err := http.Get(server.URL + "/api/metrics/feedback")
	if err != nil {
		t.Fatalf("GET /api/metrics/feedback: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Agents []struct {
			Agent      string  `json:"agent"`
			Total      int     `json:"total"`
			Accepted   int     `json:"accepted"`
			Ignored    int     `json:"ignored"`
			Pending    int     `json:"pending"`
			AcceptRate float64 `json:"accept_rate"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byAgent := map[string]struct {
		Total      int
		Accepted   int
		Ignored    int
		Pending    int
		AcceptRate float64
	}{}
	for _, a := range body.Agents {
		byAgent[a.Agent] = struct {
			Total      int
			Accepted   int
			Ignored    int
			Pending    int
			AcceptRate float64
		}{a.Total, a.Accepted, a.Ignored, a.Pending, a.AcceptRate}
	}

	sec, ok := byAgent["security"]
	if !ok {
		t.Fatalf("security stats missing: %+v", body.Agents)
	}
	if sec.Total != 3 || sec.Accepted != 2 || sec.Pending != 1 || sec.AcceptRate != 1.0 {
		t.Errorf("security = %+v, want total=3 accepted=2 pending=1 rate=1.0", sec)
	}
	logic, ok := byAgent["logic"]
	if !ok {
		t.Fatalf("logic stats missing: %+v", body.Agents)
	}
	if logic.AcceptRate != 0.5 {
		t.Errorf("logic accept rate = %v, want 0.5", logic.AcceptRate)
	}
}

// TestDashboardRendersFeedbackStats covers v1.4 AC-88: the dashboard page
// renders the per-agent adoption rates.
func TestDashboardRendersFeedbackStats(t *testing.T) {
	server, repo, fb := newDashboardHarness(t)
	ctx := context.Background()

	reviewID, err := repo.CreateReview(ctx, &repository.Review{
		RepoFullName: "fb/render", PRNumber: 32, HeadSHA: "sha-r", Status: "completed",
	})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	id1, err := repo.CreateFinding(ctx, &repository.Finding{
		ReviewID: reviewID, Agent: "security", Severity: "critical",
		FilePath: "a.go", LineNumber: 1, Message: "m", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("CreateFinding: %v", err)
	}
	id2, err := repo.CreateFinding(ctx, &repository.Finding{
		ReviewID: reviewID, Agent: "security", Severity: "warning",
		FilePath: "b.go", LineNumber: 2, Message: "m", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("CreateFinding 2: %v", err)
	}
	// 1 accepted + 1 ignored => 2 resolved, accept rate 50%.
	_, _ = fb.UpdateFindingFeedbackStatus(ctx, id1, repository.FeedbackAcceptedImplicit)
	_, _ = fb.UpdateFindingFeedbackStatus(ctx, id2, repository.FeedbackIgnoredImplicit)

	resp, err := http.Get(server.URL + "/dashboard")
	if err != nil {
		t.Fatalf("GET /dashboard: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	page := string(body)

	if !strings.Contains(page, "Agent 发现采纳率") {
		t.Errorf("page missing adoption-rate section:\n%.500s", page)
	}
	if !strings.Contains(page, "security") {
		t.Errorf("page missing the security agent row:\n%.500s", page)
	}
	// 1 of 2 resolved = 50%.
	if !strings.Contains(page, "50%") {
		t.Errorf("page missing the 50%% accept rate:\n%.500s", page)
	}
}
