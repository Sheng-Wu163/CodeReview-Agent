package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"codereviewagent/internal/orchestrator"
)

// TestSSEReviewStreamFormat verifies the v1.7 T45 completion criteria
// (AC-92/AC-93): the demo stream is served as text/event-stream with
// `data: {json}` lines whose JSON is {event: string, data: object}, ending
// with a done event.
func TestSSEReviewStreamFormat(t *testing.T) {
	broker := NewEventBroker()
	h := NewStreamReviewHandler(broker, zap.NewNop())

	server := httptest.NewServer(h)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/stream-review?pr_id=demo&demo=1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	// AC-92: SSE content type.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	// The stream must end cleanly after the done event (the server closes
	// it; a normal EOF is expected, not a timeout).
	text := string(raw)
	if text == "" {
		t.Fatal("empty SSE stream")
	}

	var (
		sawStatus, sawAgentStart, sawToolCall, sawFinding, sawDone bool
		doneAfterAll                                               bool
	)
	for _, frame := range strings.Split(text, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" || strings.HasPrefix(frame, ":") {
			continue // heartbeat comment
		}
		if !strings.HasPrefix(frame, "data: ") {
			t.Errorf("unexpected SSE frame format: %q", frame)
			continue
		}
		payload := strings.TrimPrefix(frame, "data: ")
		var ev struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Errorf("frame payload is not the {event,data} JSON: %q (%v)", payload, err)
			continue
		}
		if ev.Event == "" {
			t.Errorf("frame without event type: %q", payload)
		}
		if ev.Data == nil {
			t.Errorf("frame without data object: %q", payload)
		}

		switch ev.Event {
		case orchestrator.EventStatus:
			sawStatus = true
		case orchestrator.EventAgentStart:
			sawAgentStart = true
		case orchestrator.EventToolCall:
			sawToolCall = true
		case orchestrator.EventFinding:
			sawFinding = true
		case orchestrator.EventDone:
			sawDone = true
			doneAfterAll = sawStatus && sawAgentStart && sawToolCall && sawFinding
		}
	}

	for _, want := range []struct {
		name string
		ok   bool
	}{
		{"status", sawStatus}, {"agent_start", sawAgentStart}, {"tool_call", sawToolCall},
		{"finding", sawFinding}, {"done", sawDone},
	} {
		if !want.ok {
			t.Errorf("stream missing %q event", want.name)
		}
	}
	if !doneAfterAll {
		t.Error("done must be the last event, after all other types")
	}
}
