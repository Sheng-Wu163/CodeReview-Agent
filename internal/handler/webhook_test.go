package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"codereviewagent/internal/middleware"
)

const testSecret = "test-secret"

func serve(t *testing.T, body []byte, eventHeader string) (*httptest.ResponseRecorder, *PullRequestEvent) {
	t.Helper()

	var captured *PullRequestEvent
	// Note: in tests we capture the decoded event by wrapping the handler's
	// JSON output, mirroring what a real client would see.
	webhook := NewWebhook(zap.NewNop(), nil, nil)
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if eventHeader != "" {
		req.Header.Set("X-GitHub-Event", eventHeader)
	}
	req.Header.Set("X-Hub-Signature-256", middleware.ComputeSignature(testSecret, body))

	router := http.NewServeMux()
	handlerWithHMAC := middleware.HMACVerify(testSecret)(webhook)
	router.Handle("/webhook", handlerWithHMAC)
	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK && eventHeader == "pull_request" {
		var event PullRequestEvent
		if err := json.Unmarshal(rec.Body.Bytes(), &event); err == nil {
			captured = &event
		}
	}
	return rec, captured
}

func TestWebhookPullRequestOpened(t *testing.T) {
	body := []byte(`{"action":"opened","number":42,"pull_request":{"head":{"sha":"abc123"}},"repository":{"full_name":"foo/bar"}}`)

	rec, event := serve(t, body, "pull_request")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if event == nil {
		t.Fatalf("expected decoded pull request event, got nil")
	}
	if event.Action != "opened" {
		t.Errorf("action = %q, want %q", event.Action, "opened")
	}
	if event.Number != 42 {
		t.Errorf("number = %d, want %d", event.Number, 42)
	}
	if event.RepoFullName != "foo/bar" {
		t.Errorf("repo = %q, want %q", event.RepoFullName, "foo/bar")
	}
	if event.HeadSHA != "abc123" {
		t.Errorf("head sha = %q, want %q", event.HeadSHA, "abc123")
	}
}

func TestWebhookPullRequestSynchronize(t *testing.T) {
	body := []byte(`{"action":"synchronize","number":7,"pull_request":{"head":{"sha":"deadbeef"}},"repository":{"full_name":"a/b"}}`)

	rec, event := serve(t, body, "pull_request")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if event == nil || event.Action != "synchronize" {
		t.Fatalf("expected synchronize event to be handled, got %+v", event)
	}
}

func TestWebhookPullRequestIgnoredAction(t *testing.T) {
	body := []byte(`{"action":"assigned","number":7,"pull_request":{"head":{"sha":"x"}},"repository":{"full_name":"a/b"}}`)

	rec, _ := serve(t, body, "pull_request")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "ignored") {
		t.Errorf("expected ignored response, got %q", rec.Body.String())
	}
}

// mockFeedbackTracker records the tracker calls (v1.4 tests); each channel
// receives the call arguments so tests can wait for the async dispatch.
type mockFeedbackTracker struct {
	syncCalled   chan syncCall
	closedCalled chan closedCall
}

type syncCall struct {
	repo           string
	prNumber       int
	newSHA, oldSHA string
}

type closedCall struct {
	repo     string
	prNumber int
	isMerged bool
}

func newMockFeedbackTracker() *mockFeedbackTracker {
	return &mockFeedbackTracker{
		syncCalled:   make(chan syncCall, 1),
		closedCalled: make(chan closedCall, 1),
	}
}

func (m *mockFeedbackTracker) TrackOnSynchronize(_ context.Context, repo string, prNumber int, newSHA string, oldSHA string) error {
	m.syncCalled <- syncCall{repo: repo, prNumber: prNumber, newSHA: newSHA, oldSHA: oldSHA}
	return nil
}

func (m *mockFeedbackTracker) TrackOnClosed(_ context.Context, repo string, prNumber int, isMerged bool) error {
	m.closedCalled <- closedCall{repo: repo, prNumber: prNumber, isMerged: isMerged}
	return nil
}

// TestWebhookSynchronizeTriggersTracker covers AC-82: a synchronize event
// extracts before/after and calls TrackOnSynchronize.
func TestWebhookSynchronizeTriggersTracker(t *testing.T) {
	tracker := newMockFeedbackTracker()
	webhook := NewWebhook(zap.NewNop(), nil, tracker)

	body := []byte(`{"action":"synchronize","number":7,"before":"old-sha","after":"new-sha","pull_request":{"head":{"sha":"new-sha"}},"repository":{"full_name":"a/b"}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", middleware.ComputeSignature(testSecret, body))
	rec := httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	select {
	case call := <-tracker.syncCalled:
		if call.repo != "a/b" || call.prNumber != 7 {
			t.Errorf("call = %+v, want repo a/b pr 7", call)
		}
		if call.newSHA != "new-sha" || call.oldSHA != "old-sha" {
			t.Errorf("SHAs = new:%q old:%q, want new-sha/old-sha (after/before)", call.newSHA, call.oldSHA)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TrackOnSynchronize was not called")
	}
}

// TestWebhookClosedTriggersTracker covers AC-83: a closed (merged) event
// calls TrackOnClosed.
func TestWebhookClosedTriggersTracker(t *testing.T) {
	tracker := newMockFeedbackTracker()
	webhook := NewWebhook(zap.NewNop(), nil, tracker)

	body := []byte(`{"action":"closed","number":9,"pull_request":{"merged":true,"head":{"sha":"x"}},"repository":{"full_name":"a/b"}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", middleware.ComputeSignature(testSecret, body))
	rec := httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	select {
	case call := <-tracker.closedCalled:
		if call.repo != "a/b" || call.prNumber != 9 || !call.isMerged {
			t.Errorf("call = %+v, want repo a/b pr 9 merged=true", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TrackOnClosed was not called")
	}
}

func TestWebhookUnknownEventIgnored(t *testing.T) {
	body := []byte(`{"zen":"Design is not just what it looks like."}`)

	rec, _ := serve(t, body, "ping")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "ping") {
		t.Errorf("expected event name in response, got %q", rec.Body.String())
	}
}

func TestWebhookInvalidPayload(t *testing.T) {
	body := []byte(`not-json{`)

	rec, _ := serve(t, body, "pull_request")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestWebhookInvalidSignature(t *testing.T) {
	body := []byte(`{"action":"opened","number":1,"pull_request":{"head":{"sha":"s"}},"repository":{"full_name":"a/b"}}`)

	webhook := NewWebhook(zap.NewNop(), nil, nil)
	middlewareWithHMAC := middleware.HMACVerify(testSecret)(webhook)

	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("f", 64))
	rec := httptest.NewRecorder()

	middlewareWithHMAC.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestWebhookMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)

	NewWebhook(zap.NewNop(), nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
