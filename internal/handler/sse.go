package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"codereviewagent/internal/orchestrator"
)

// EventBroker fans review-run events out to every subscribed SSE client
// (v1.7 FR-44). Reviews are keyed by topic, e.g. "owner/repo#42" or a demo
// topic.
type EventBroker struct {
	mu   sync.Mutex
	subs map[string]map[chan orchestrator.ReviewEvent]struct{}
}

// NewEventBroker creates an empty broker.
func NewEventBroker() *EventBroker {
	return &EventBroker{subs: make(map[string]map[chan orchestrator.ReviewEvent]struct{})}
}

// Subscribe registers a channel for the topic and returns an unsubscribe
// function.
func (b *EventBroker) Subscribe(topic string) (chan orchestrator.ReviewEvent, func()) {
	ch := make(chan orchestrator.ReviewEvent, 256)
	b.mu.Lock()
	if b.subs[topic] == nil {
		b.subs[topic] = make(map[chan orchestrator.ReviewEvent]struct{})
	}
	b.subs[topic][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs[topic], ch)
		b.mu.Unlock()
	}
}

// Publish delivers ev to every subscriber of the topic without blocking
// (a slow SSE client drops events instead of stalling the review).
func (b *EventBroker) Publish(topic string, ev orchestrator.ReviewEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[topic] {
		select {
		case ch <- ev:
		default:
		}
	}
}

// demoReviewEvents is the scripted simulated review used by the
// "开始审查" demo trigger (FR-46 left pane): it exercises all five event
// types deterministically, including a markdown report whose text embeds a
// <script> tag so the XSS sanitisation is verifiable (spec 16.2).
func demoReviewEvents() []orchestrator.ReviewEvent {
	md := "## 审查结果（1 条发现）\n\n" +
		"### CRITICAL `auth/login.go:12`\n\n" +
		"SQL 注入：查询由用户输入拼接构建，攻击者可注入任意 SQL。\n\n" +
		"```go\nrows, err := db.Query(\"SELECT * FROM users WHERE name = '\" + user + \"'\")\n```\n\n" +
		"**建议**: 使用参数化查询 `db.Query(\"... WHERE name = ?\", user)`。\n\n" +
		"恶意输入回归测试: <script>alert(1)</script> 与 <img src=x onerror=alert(1)>\n"

	return []orchestrator.ReviewEvent{
		{Event: orchestrator.EventStatus, Data: map[string]any{
			"stage": "strategy", "message": "复杂度评估: standard",
		}},
		{Event: orchestrator.EventStatus, Data: map[string]any{
			"stage": "static-analysis", "message": "正在运行静态分析...",
		}},
		{Event: orchestrator.EventStatus, Data: map[string]any{
			"stage": "static-analysis", "message": "静态分析完成，发现 1 条已知问题", "findings": 1,
		}},
		{Event: orchestrator.EventAgentStart, Data: map[string]any{"agent": "security"}},
		{Event: orchestrator.EventToolCall, Data: map[string]any{
			"agent": "security", "tool": "read_file", "input": "auth/login.go:1:30",
		}},
		{Event: orchestrator.EventToolCall, Data: map[string]any{
			"agent": "security", "tool": "search_code", "input": "db.Query pattern=auth/*.go",
		}},
		{Event: orchestrator.EventFinding, Data: map[string]any{
			"agent": "security", "severity": "critical", "file_path": "auth/login.go",
			"line_number": 12, "message": "SQL 注入：查询由字符串拼接构建（`+ user +`），未使用参数化查询。",
			"code_snippet": "rows, err := db.Query(\"SELECT * FROM users WHERE name = '\" + user + \"'\")",
			"confidence":   0.97,
		}},
		{Event: orchestrator.EventAgentStart, Data: map[string]any{"agent": "lint"}},
		{Event: orchestrator.EventToolCall, Data: map[string]any{
			"agent": "lint", "tool": "get_function_definition", "input": "Login",
		}},
		{Event: orchestrator.EventDone, Data: map[string]any{
			"findings": 1, "duration_ms": 4200, "markdown": md,
		}},
	}
}

// StreamReviewHandler serves GET /api/stream-review?pr_id={id}
// (v1.7 FR-44): it subscribes to the review's event topic over SSE and
// writes `data: {json}\n\n` lines, flushing after each event
// (CLAUDE.v.1.7.md). pr_id=demo plays the scripted simulated review.
type StreamReviewHandler struct {
	broker *EventBroker
	logger *zap.Logger
}

// NewStreamReviewHandler creates the SSE handler.
func NewStreamReviewHandler(broker *EventBroker, logger *zap.Logger) *StreamReviewHandler {
	return &StreamReviewHandler{broker: broker, logger: logger}
}

// heartbeatInterval keeps idle connections alive so intermediaries do not
// time the request out (T45 completion: no HTTP timeout).
const heartbeatInterval = 15 * time.Second

// ServeHTTP implements http.Handler.
func (h *StreamReviewHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	prID := r.URL.Query().Get("pr_id")
	if prID == "" {
		http.Error(w, "missing pr_id", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, unsubscribe := h.broker.Subscribe(prID)
	defer unsubscribe()

	// The demo trigger plays the scripted review on this topic (FR-46).
	if r.URL.Query().Get("demo") == "1" {
		go func() {
			for _, ev := range demoReviewEvents() {
				h.broker.Publish(prID, ev)
				time.Sleep(400 * time.Millisecond)
			}
		}()
	}

	keepalive := time.NewTicker(heartbeatInterval)
	defer keepalive.Stop()

	writeEvent := func(ev orchestrator.ReviewEvent) bool {
		data, err := json.Marshal(ev)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for {
		select {
		case <-r.Context().Done():
			// Client disconnected: free the resources (CLAUDE.v.1.7.md).
			return
		case <-keepalive.C:
			// SSE comment line as heartbeat.
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !writeEvent(ev) {
				return
			}
			if ev.Event == orchestrator.EventDone {
				// The run finished: end the stream cleanly.
				return
			}
		}
	}
}

// MountStreamReview registers the SSE route on r.
func MountStreamReview(r chi.Router, broker *EventBroker, logger *zap.Logger) {
	r.Get("/api/stream-review", NewStreamReviewHandler(broker, logger).ServeHTTP)
}
