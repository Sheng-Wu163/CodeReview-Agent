// Package handler provides HTTP request handlers for the service.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// PullRequestEvent carries the fields extracted from a GitHub
// pull_request webhook payload (FR-02).
type PullRequestEvent struct {
	// Action is the event action, e.g. "opened" or "synchronize".
	Action string `json:"action"`
	// Number is the PR number.
	Number int `json:"number"`
	// RepoFullName is the repository full name, e.g. "owner/repo".
	RepoFullName string `json:"repo_full_name"`
	// HeadSHA is the head commit SHA of the PR.
	HeadSHA string `json:"head_sha"`
}

// PullRequestProcessor handles accepted pull_request events
// (opened/synchronize). Implemented by the orchestrator pipeline; wired
// asynchronously by main so the webhook acknowledges quickly (AC-03).
type PullRequestProcessor interface {
	Process(ctx context.Context, event PullRequestEvent)
}

// Webhook handles GitHub webhook events. It expects the request body to have
// already passed HMAC signature verification (see middleware.HMACVerify).
type Webhook struct {
	logger    *zap.Logger
	processor PullRequestProcessor // may be nil
}

// NewWebhook creates a new Webhook handler. processor may be nil, in which
// case accepted pull_request events are only logged.
func NewWebhook(logger *zap.Logger, processor PullRequestProcessor) *Webhook {
	return &Webhook{logger: logger, processor: processor}
}

// pullRequestPayload is the subset of the GitHub pull_request webhook payload
// that this handler needs to parse.
type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// ServeHTTP implements http.Handler. Only POST requests are accepted.
func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")

	body := make([]byte, r.ContentLength)
	if r.ContentLength > 0 {
		if _, err := r.Body.Read(body); err != nil && err.Error() != "EOF" {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
	}

	switch eventType {
	case "pull_request":
		h.handlePullRequest(w, r, body)
	default:
		h.logger.Sugar().Infow("ignored webhook event", "event", eventType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf("event %s ignored", eventType)))
	}
}

// handlePullRequest processes pull_request events. Only the "opened" and
// "synchronize" actions trigger the review flow (FR-02).
func (h *Webhook) handlePullRequest(w http.ResponseWriter, r *http.Request, body []byte) {
	var payload pullRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if payload.Action != "opened" && payload.Action != "synchronize" {
		h.logger.Sugar().Infow("ignored pull_request action", "action", payload.Action)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf("pull_request action %s ignored", payload.Action)))
		return
	}

	event := PullRequestEvent{
		Action:       payload.Action,
		Number:       payload.Number,
		RepoFullName: payload.Repository.FullName,
		HeadSHA:      payload.PullRequest.Head.SHA,
	}
	h.logger.Sugar().Infow("pull_request event received, triggering review",
		"action", event.Action,
		"pr_number", event.Number,
		"repo", event.RepoFullName,
		"head_sha", event.HeadSHA,
	)

	// Review runs in the background: GitHub expects a fast webhook
	// acknowledgment, and the webhook secret does not allow replay anyway.
	if h.processor != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			h.processor.Process(ctx, event)
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(event)
}
