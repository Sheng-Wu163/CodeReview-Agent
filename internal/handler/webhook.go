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

// FeedbackTracker handles the synchronize/closed PR events for implicit
// feedback tracking (v1.4 FR-41); feedback.Tracker implements it. Defined
// in the consumer per convention.
type FeedbackTracker interface {
	TrackOnSynchronize(ctx context.Context, repo string, prNumber int, newSHA string, oldSHA string) error
	TrackOnClosed(ctx context.Context, repo string, prNumber int, isMerged bool) error
}

// Webhook handles GitHub webhook events. It expects the request body to have
// already passed HMAC signature verification (see middleware.HMACVerify).
type Webhook struct {
	logger    *zap.Logger
	processor PullRequestProcessor // may be nil
	tracker   FeedbackTracker      // may be nil (v1.4 implicit feedback)
}

// NewWebhook creates a new Webhook handler. processor and tracker may be
// nil, in which case the corresponding events are only logged.
func NewWebhook(logger *zap.Logger, processor PullRequestProcessor, tracker FeedbackTracker) *Webhook {
	return &Webhook{logger: logger, processor: processor, tracker: tracker}
}

// pullRequestPayload is the subset of the GitHub pull_request webhook payload
// that this handler needs to parse.
type pullRequestPayload struct {
	Action string `json:"action"`
	Number int    `json:"number"`
	// Before/After are the push SHAs of a synchronize event (FR-40).
	Before      string `json:"before"`
	After       string `json:"after"`
	PullRequest struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		// Merged is true when a closed event actually merged the PR.
		Merged bool `json:"merged"`
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
		_, _ = fmt.Fprintf(w, "event %s ignored", eventType)
	}
}

// handlePullRequest processes pull_request events. "opened" and
// "synchronize" trigger the review flow (FR-02); "synchronize" and "closed"
// additionally feed the implicit feedback tracker (v1.4 FR-40/FR-41).
func (h *Webhook) handlePullRequest(w http.ResponseWriter, _ *http.Request, body []byte) {
	var payload pullRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	switch payload.Action {
	case "opened", "synchronize":
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
		// v1.4: a synchronize event also advances the implicit feedback
		// state machine (new code pushed after the review).
		if payload.Action == "synchronize" && h.tracker != nil && payload.After != "" {
			h.dispatchTrackOnSynchronize(payload)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(event)

	case "closed":
		// v1.4 FR-40: PR closed (merged or not) resolves the remaining
		// pending findings as ignored_implicit.
		h.logger.Sugar().Infow("pull_request closed, tracking implicit feedback",
			"pr_number", payload.Number,
			"repo", payload.Repository.FullName,
			"merged", payload.PullRequest.Merged)
		if h.tracker != nil {
			h.dispatchTrackOnClosed(payload)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"action": payload.Action, "pr_number": payload.Number,
			"repo": payload.Repository.FullName, "tracked": h.tracker != nil,
		})

	default:
		h.logger.Sugar().Infow("ignored pull_request action", "action", payload.Action)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "pull_request action %s ignored", payload.Action)
	}
}

// dispatchTrackOnSynchronize hands a synchronize event to the tracker in
// the background (FR-40).
func (h *Webhook) dispatchTrackOnSynchronize(payload pullRequestPayload) {
	tracker := h.tracker
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := tracker.TrackOnSynchronize(ctx, payload.Repository.FullName, payload.Number, payload.After, payload.Before); err != nil {
			h.logger.Sugar().Errorw("implicit feedback tracking failed",
				"pr_number", payload.Number, "repo", payload.Repository.FullName, "error", err)
		}
	}()
}

// dispatchTrackOnClosed hands a closed event to the tracker in the
// background (FR-40).
func (h *Webhook) dispatchTrackOnClosed(payload pullRequestPayload) {
	tracker := h.tracker
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := tracker.TrackOnClosed(ctx, payload.Repository.FullName, payload.Number, payload.PullRequest.Merged); err != nil {
			h.logger.Sugar().Errorw("implicit feedback close tracking failed",
				"pr_number", payload.Number, "repo", payload.Repository.FullName, "error", err)
		}
	}()
}
