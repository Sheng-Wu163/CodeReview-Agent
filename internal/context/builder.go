package context

import (
	"context"

	"codereviewagent/internal/diff"
)

// ContextBuilder builds the local context bundle for a review run
// (v1.1 spec 12.3, FROZEN). It never fails the review: on any lookup error
// it degrades to an empty bundle and records a warning (FR-29).
type ContextBuilder interface { //nolint:revive // name is frozen by spec 12.3
	// Build inspects the diffs and returns the local context bundle.
	Build(ctx context.Context, diffs []diff.FileDiff) (ContextBundle, error)
}
