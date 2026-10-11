package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"codereviewagent/internal/middleware"
)

const testSecret = "test-secret"

func serve(t *testing.T, body []byte, eventHeader string) (*httptest.ResponseRecorder, *PullRequestEvent) {
	t.Helper()

	var captured *PullRequestEvent
	// Note: in tests we capture the decoded event by wrapping the handler's
	// JSON output, mirroring what a real client would see.
	webhook := NewWebhook(zap.NewNop(), nil)
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
	body := []byte(`{"action":"closed","number":7,"pull_request":{"head":{"sha":"x"}},"repository":{"full_name":"a/b"}}`)

	rec, _ := serve(t, body, "pull_request")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "ignored") {
		t.Errorf("expected ignored response, got %q", rec.Body.String())
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

	webhook := NewWebhook(zap.NewNop(), nil)
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

	NewWebhook(zap.NewNop(), nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
