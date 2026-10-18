// Package feedback implements the v1.4 implicit feedback tracking: it
// infers the developer's attitude towards review findings from subsequent
// PR activity (synchronize pushes and merges) without asking, building an
// unbiased feedback dataset (spec 15.1).
//
// The public types below are FROZEN as of v1.4 (spec 15.3). Do not change
// them without a change proposal under specs/changes/.
package feedback

import (
	"context"

	"codereviewagent/internal/repository"
)

// FeedbackStatus is the feedback state of a finding (v1.4 spec 15.3,
// FROZEN).
type FeedbackStatus string //nolint:revive // name is frozen by spec 15.3

// Feedback states. accepted_explicit / rejected_explicit are reserved for a
// future explicit-feedback iteration (spec 15.3).
const (
	// StatusPending means the outcome is not yet known.
	StatusPending FeedbackStatus = "pending"
	// StatusAcceptedImplicit means the tracked code was subsequently
	// modified (or its file deleted) - the developer acted on it.
	StatusAcceptedImplicit FeedbackStatus = "accepted_implicit"
	// StatusIgnoredImplicit means the PR was closed while the tracked code
	// stayed untouched.
	StatusIgnoredImplicit FeedbackStatus = "ignored_implicit"
)

// PRCommit records one commit of a pull request (v1.4 spec 15.3, FROZEN).
type PRCommit = repository.PRCommit

// FeedbackTracker tracks implicit feedback for a PR's findings (v1.4 spec
// 15.3, FROZEN).
type FeedbackTracker interface { //nolint:revive // name is frozen by spec 15.3
	// TrackOnSynchronize compares the code at the new and old SHAs and
	// resolves pending findings whose tracked code was modified.
	TrackOnSynchronize(ctx context.Context, repo string, prNumber int, newSHA string, oldSHA string) error
	// TrackOnClosed resolves the remaining pending findings when the PR is
	// closed.
	TrackOnClosed(ctx context.Context, repo string, prNumber int, isMerged bool) error
}
