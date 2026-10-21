package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"codereviewagent/internal/ai/agents"
)

// mockServerState records what the mock GitHub API received.
type mockServerState struct {
	mu       sync.Mutex
	diffBody string
	reviews  []map[string]any
	statuses []map[string]any
	prBody   map[string]any
}

func (s *mockServerState) addReview(m map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviews = append(s.reviews, m)
}

func (s *mockServerState) addStatus(m map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, m)
}

// newMockClient spins up an httptest server implementing the GitHub API
// surface used by the client and returns a Client bound to it.
func newMockClient(t *testing.T, state *mockServerState) *Client {
	t.Helper()

	mux := http.NewServeMux()

	// GET /repos/{owner}/{repo}/pulls/{n} -> PR JSON with head SHA.
	mux.HandleFunc("/repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 1,
			"head":   map[string]any{"sha": "head-sha-1"},
		})
	})

	// GET /repos/{owner}/{repo}/pulls/{n} with diff Accept header.
	mux.HandleFunc("/repos/o/r/pulls/2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(state.diffBody))
	})

	// POST /repos/{owner}/{repo}/pulls/{n}/reviews
	mux.HandleFunc("/repos/o/r/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var review map[string]any
		if err := json.Unmarshal(body, &review); err != nil {
			t.Errorf("invalid review JSON: %v", err)
		}
		state.addReview(review)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})

	// POST /repos/{owner}/{repo}/statuses/{sha}
	mux.HandleFunc("/repos/o/r/statuses/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var status map[string]any
		if err := json.Unmarshal(body, &status); err != nil {
			t.Errorf("invalid status JSON: %v", err)
		}
		state.addStatus(status)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return NewClientWithEndpoint("test-token", server.URL+"/")
}

func TestGetPRDiff(t *testing.T) {
	state := &mockServerState{diffBody: "diff --git a/main.go b/main.go\n+++ b/main.go\n"}
	c := newMockClient(t, state)

	got, err := c.GetPRDiff(context.Background(), "o", "r", 2)
	if err != nil {
		t.Fatalf("GetPRDiff: %v", err)
	}
	if got != state.diffBody {
		t.Errorf("GetPRDiff = %q, want %q", got, state.diffBody)
	}
}

func TestGetPRHeadSHA(t *testing.T) {
	c := newMockClient(t, &mockServerState{})

	sha, err := c.GetPRHeadSHA(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatalf("GetPRHeadSHA: %v", err)
	}
	if sha != "head-sha-1" {
		t.Errorf("SHA = %q, want head-sha-1", sha)
	}
}

func TestSubmitFindingsReview(t *testing.T) {
	state := &mockServerState{}
	c := newMockClient(t, state)

	findings := []agents.VerifiedFinding{
		{AgentFinding: agents.AgentFinding{
			Agent: "logic", Severity: agents.SeverityWarning,
			FilePath: "internal/api/decode.go", LineNumber: 3,
			Message:     "unhandled json.Unmarshal error",
			CodeSnippet: "json.Unmarshal(data, &result)",
			Suggestion:  "check the returned error",
			Confidence:  0.9,
		}, Confidence: 0.9},
		{AgentFinding: agents.AgentFinding{
			Agent: "lint", Severity: agents.SeveritySuggestion,
			FilePath: "internal/service/user_service.go", LineNumber: 4,
			Message:    "Get_User_Data violates naming conventions",
			Suggestion: "rename to getUserData",
			Confidence: 0.85,
		}, Confidence: 0.85},
		{AgentFinding: agents.AgentFinding{
			Agent: "performance", Severity: agents.SeverityWarning,
			FilePath: "internal/report/render.go", LineNumber: 8,
			Message:    "string += in loop",
			Confidence: 0.88,
		}, Confidence: 0.88},
	}

	event, err := c.SubmitFindingsReview(context.Background(), "o", "r", 1, "sha-abc", findings)
	if err != nil {
		t.Fatalf("SubmitFindingsReview: %v", err)
	}

	// AC-15: one review, summary body, 3 inline comments.
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.reviews) != 1 {
		t.Fatalf("reviews = %d, want 1", len(state.reviews))
	}
	review := state.reviews[0]
	if review["body"] != BuildSummary(findings) {
		t.Errorf("summary = %q, want %q", review["body"], BuildSummary(findings))
	}
	comments, ok := review["comments"].([]any)
	if !ok || len(comments) != 3 {
		t.Fatalf("comments = %v, want 3 inline comments", review["comments"])
	}
	first := comments[0].(map[string]any)
	if first["path"] != "internal/api/decode.go" {
		t.Errorf("comment[0].path = %v", first["path"])
	}
	if first["line"].(float64) != 3 {
		t.Errorf("comment[0].line = %v, want 3", first["line"])
	}
	body := first["body"].(string)
	for _, want := range []string{"[warning]", "unhandled json.Unmarshal error", "Suggestion", "0.90", "logic"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment body %q missing %q", body, want)
		}
	}
	// No critical findings -> COMMENT event.
	if event != reviewEventComment {
		t.Errorf("event = %q, want COMMENT", event)
	}
}

func TestSubmitFindingsReviewRequestChangesOnCritical(t *testing.T) {
	state := &mockServerState{}
	c := newMockClient(t, state)

	findings := []agents.VerifiedFinding{
		{AgentFinding: agents.AgentFinding{
			Agent: "security", Severity: agents.SeverityCritical,
			FilePath: "users.go", LineNumber: 4,
			Message: "SQL injection", Confidence: 0.95,
		}, Confidence: 0.95},
	}

	event, err := c.SubmitFindingsReview(context.Background(), "o", "r", 1, "sha-abc", findings)
	if err != nil {
		t.Fatalf("SubmitFindingsReview: %v", err)
	}
	if event != reviewEventRequestChanges {
		t.Errorf("event = %q, want REQUEST_CHANGES for critical finding", event)
	}
}

func TestSubmitLGTMReview(t *testing.T) {
	state := &mockServerState{}
	c := newMockClient(t, state)

	if err := c.SubmitLGTMReview(context.Background(), "o", "r", 1, "sha-abc"); err != nil {
		t.Fatalf("SubmitLGTMReview: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.reviews) != 1 {
		t.Fatalf("reviews = %d, want 1", len(state.reviews))
	}
	if state.reviews[0]["body"] != "LGTM ✅" {
		t.Errorf("body = %q, want LGTM review (AC-17)", state.reviews[0]["body"])
	}
}

func TestSubmitFindingsReviewWithWarnings(t *testing.T) {
	state := &mockServerState{}
	c := newMockClient(t, state)

	warnings := []string{"⚠️ 安全审查未完成（security: failed - timeout）"}
	event, err := c.SubmitFindingsReviewWithWarnings(
		context.Background(), "o", "r", 1, "sha-abc", nil, warnings)
	if err != nil {
		t.Fatalf("SubmitFindingsReviewWithWarnings: %v", err)
	}
	if event != reviewEventComment {
		t.Errorf("event = %q, want COMMENT", event)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.reviews) != 1 {
		t.Fatalf("reviews = %d, want 1", len(state.reviews))
	}
	body, _ := state.reviews[0]["body"].(string)
	if !strings.Contains(body, "安全审查未完成") {
		t.Errorf("body = %q, want security warning (AC-106)", body)
	}
}

func TestUpdateCommitStatus(t *testing.T) {
	state := &mockServerState{}
	c := newMockClient(t, state)

	if err := c.UpdateCommitStatus(context.Background(), "o", "r", "sha-abc", StatusFailure, "1 critical finding"); err != nil {
		t.Fatalf("UpdateCommitStatus: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(state.statuses))
	}
	status := state.statuses[0]
	if status["state"] != StatusFailure {
		t.Errorf("state = %v, want failure (AC-11)", status["state"])
	}
	if status["context"] != statusContext {
		t.Errorf("context = %v, want %q", status["context"], statusContext)
	}
	if status["description"] != "1 critical finding" {
		t.Errorf("description = %v", status["description"])
	}
}

func TestStatusForFindings(t *testing.T) {
	critical := []agents.VerifiedFinding{
		{AgentFinding: agents.AgentFinding{Severity: agents.SeverityCritical, FilePath: "a", LineNumber: 1, Message: "x"}, Confidence: 0.9},
	}
	warnings := []agents.VerifiedFinding{
		{AgentFinding: agents.AgentFinding{Severity: agents.SeverityWarning, FilePath: "a", LineNumber: 1, Message: "x"}, Confidence: 0.9},
	}

	state, desc := StatusForFindings(critical)
	if state != StatusFailure {
		t.Errorf("state = %q, want failure (AC-11)", state)
	}
	if !strings.Contains(desc, "1 critical") {
		t.Errorf("description = %q, want critical count", desc)
	}

	state, _ = StatusForFindings(warnings)
	if state != StatusSuccess {
		t.Errorf("state = %q, want success for non-critical findings", state)
	}

	state, _ = StatusForFindings(nil)
	if state != StatusSuccess {
		t.Errorf("state = %q, want success for no findings (AC-17)", state)
	}
}

func TestFormatFinding(t *testing.T) {
	f := agents.VerifiedFinding{AgentFinding: agents.AgentFinding{
		Agent: "security", Severity: agents.SeverityCritical,
		FilePath: "users.go", LineNumber: 4,
		Message:     "SQL injection",
		CodeSnippet: "db.Query(x + y)",
		Suggestion:  "parameterize",
		Confidence:  0.95,
	}, Confidence: 0.95}

	body := FormatFinding(f)
	for _, want := range []string{
		"**[critical] SQL injection**",
		"```",
		"db.Query(x + y)",
		"**Suggestion:** parameterize",
		"Confidence: 0.95",
		"Agent: security",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("FormatFinding missing %q:\n%s", want, body)
		}
	}
}
