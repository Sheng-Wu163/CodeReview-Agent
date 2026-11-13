package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"codereviewagent/internal/ai/agents"
	"codereviewagent/internal/review"
)

// TestListReviewThreadsParsesFingerprint covers T69: the GraphQL response is
// mapped to ThreadState including the hidden fingerprint marker.
func TestListReviewThreadsParsesFingerprint(t *testing.T) {
	var mu sync.Mutex
	var mutationVariables map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(req.Query, "resolveReviewThread") {
			mu.Lock()
			mutationVariables = req.Variables
			mu.Unlock()
			_, _ = w.Write([]byte(`{"data":{"resolveReviewThread":{"thread":{"id":"t1","isResolved":true}}}}`))
			return
		}

		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
			{"id":"t1","isResolved":false,"isOutdated":true,"comments":{"nodes":[
				{"path":"users.go","line":12,"body":"**[warning] SQL\n\n<!-- cra-fp:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -->"}
			]}},
			{"id":"t2","isResolved":true,"isOutdated":false,"comments":{"nodes":[
				{"path":"main.go","line":3,"body":"no marker"}
			]}}
		]}}}}}`))
	}))
	defer server.Close()

	client := NewClientWithEndpoint("token", server.URL+"/")
	threads, err := client.ListReviewThreads(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatalf("ListReviewThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads = %d, want 2", len(threads))
	}
	first := threads[0]
	if first.ThreadID != "t1" || first.Path != "users.go" || first.Line != 12 {
		t.Errorf("thread[0] = %+v", first)
	}
	if first.Fingerprint != strings.Repeat("a", 64) {
		t.Errorf("fingerprint = %q, want parsed marker", first.Fingerprint)
	}
	if !first.IsOutdated || first.IsResolved {
		t.Errorf("thread[0] flags = resolved:%v outdated:%v", first.IsResolved, first.IsOutdated)
	}
	if !threads[1].IsResolved || threads[1].Fingerprint != "" {
		t.Errorf("thread[1] = %+v", threads[1])
	}

	if err := client.ResolveReviewThread(context.Background(), "t1"); err != nil {
		t.Fatalf("ResolveReviewThread: %v", err)
	}
	mu.Lock()
	threadID := mutationVariables["threadId"]
	mu.Unlock()
	if threadID != "t1" {
		t.Errorf("mutation threadId = %v, want t1", threadID)
	}
}

func verifiedFindingForTest() agents.VerifiedFinding {
	return agents.VerifiedFinding{
		AgentFinding: agents.AgentFinding{
			Agent: "security", Severity: agents.SeverityCritical,
			FilePath: "users.go", LineNumber: 12, Message: "SQL injection", Confidence: 0.9,
		},
		Confidence: 0.9,
	}
}

// TestFormatFindingEmbedsFingerprint covers the publish marker (FR-67).
func TestFormatFindingEmbedsFingerprint(t *testing.T) {
	f := verifiedFindingForTest()
	body := FormatFinding(f)
	want := "<!-- cra-fp:" + review.EffectiveFingerprint(f) + " -->"
	if !strings.Contains(body, want) {
		t.Errorf("comment body missing fingerprint marker %q: %q", want, body)
	}
}
