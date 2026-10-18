package feedback

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"codereviewagent/internal/github"
	"codereviewagent/internal/repository"
)

// snippetWindow is the number of context lines extracted on each side of a
// finding's line for the local comparison (v1.4 FR-42: the finding plus the
// ~10 surrounding code lines).
const snippetWindow = 5

// GitHubClient fetches file contents at a ref (v1.4 FR-41); implemented by
// github.Client. Defined in the consumer per convention
// (CLAUDE.v.1.4.md: no raw HTTP in the tracker).
type GitHubClient interface {
	GetFileAtRef(ctx context.Context, owner, repo, path, ref string) (string, error)
}

// Compile-time check that github.Client implements GitHubClient.
var _ GitHubClient = (*github.Client)(nil)

// Tracker is the default FeedbackTracker (v1.4 FR-41). It resolves pending
// findings based on whether their tracked code changed between two commits.
type Tracker struct {
	repo      *repository.FeedbackRepo
	gh        GitHubClient
	threshold float64
	logger    *zap.Logger
}

// Compile-time check that Tracker implements FeedbackTracker.
var _ FeedbackTracker = (*Tracker)(nil)

// NewTracker creates a Tracker. threshold <= 0 or >= 1 falls back to
// DefaultSimilarityThreshold. gh must be a github.Client wrapper
// (mocked in tests).
func NewTracker(repo *repository.FeedbackRepo, gh GitHubClient, threshold float64, logger *zap.Logger) *Tracker {
	if threshold <= 0 || threshold >= 1 {
		threshold = DefaultSimilarityThreshold
	}
	return &Tracker{repo: repo, gh: gh, threshold: threshold, logger: logger}
}

// TrackOnSynchronize implements FeedbackTracker: for every pending finding
// of the PR it compares the ±snippetWindow lines around the finding between
// the old and the new commit; a similarity below the threshold means the
// code was modified and the finding becomes accepted_implicit (FR-41/42).
// Per-finding failures are logged and skipped - the batch never fails.
func (t *Tracker) TrackOnSynchronize(ctx context.Context, repo string, prNumber int, newSHA string, oldSHA string) error {
	owner, name, ok := splitRepo(repo)
	if !ok {
		t.logger.Sugar().Warnw("feedback: invalid repo full name", "repo", repo)
		return nil
	}

	// Record the commit history (FR-39); duplicates are deduplicated.
	for _, sha := range []string{oldSHA, newSHA} {
		if sha == "" {
			continue
		}
		if err := t.repo.RecordPRCommit(ctx, repo, prNumber, sha); err != nil {
			t.logger.Sugar().Warnw("feedback: record commit failed", "error", err)
		}
	}

	findings, err := t.repo.ListPendingFindingsByPR(ctx, repo, prNumber)
	if err != nil {
		return fmt.Errorf("list pending findings: %w", err)
	}
	if len(findings) == 0 {
		return nil
	}

	for _, f := range findings {
		oldContent, err := t.gh.GetFileAtRef(ctx, owner, name, f.FilePath, oldSHA)
		if err != nil {
			if errors.Is(err, github.ErrFileNotFound) {
				t.logger.Sugar().Warnw("feedback: old file version missing, skipping finding",
					"finding", f.ID, "file", f.FilePath)
				continue
			}
			t.logger.Sugar().Warnw("feedback: fetch old file failed, skipping finding",
				"finding", f.ID, "file", f.FilePath, "error", err)
			continue
		}
		newContent, err := t.gh.GetFileAtRef(ctx, owner, name, f.FilePath, newSHA)
		if err != nil {
			if errors.Is(err, github.ErrFileNotFound) {
				// The whole file was deleted: the problem is gone
				// (spec 15.4) - count as accepted.
				t.resolve(ctx, f, StatusAcceptedImplicit, "file deleted")
				continue
			}
			t.logger.Sugar().Warnw("feedback: fetch new file failed, skipping finding",
				"finding", f.ID, "file", f.FilePath, "error", err)
			continue
		}

		oldSnippet := windowLines(oldContent, f.LineNumber, snippetWindow)
		newSnippet := windowLines(newContent, f.LineNumber, snippetWindow)
		sim := Similarity(oldSnippet, newSnippet)
		if sim < t.threshold {
			t.resolve(ctx, f, StatusAcceptedImplicit, fmt.Sprintf("similarity %.2f < %.2f", sim, t.threshold))
		} else {
			t.logger.Sugar().Infow("feedback: tracked code unchanged, staying pending",
				"finding", f.ID, "similarity", sim)
		}
	}
	return nil
}

// TrackOnClosed implements FeedbackTracker: every finding still pending is
// resolved as ignored_implicit - merged PRs kept the code untouched, and
// directly closed PRs never acted on the findings either (spec 15.4). The
// guarded update makes repeated runs idempotent.
func (t *Tracker) TrackOnClosed(ctx context.Context, repo string, prNumber int, isMerged bool) error {
	findings, err := t.repo.ListPendingFindingsByPR(ctx, repo, prNumber)
	if err != nil {
		return fmt.Errorf("list pending findings: %w", err)
	}
	for _, f := range findings {
		t.resolve(ctx, f, StatusIgnoredImplicit, fmt.Sprintf("PR closed (merged=%v)", isMerged))
	}
	return nil
}

// resolve transitions one finding if it is still pending (idempotent,
// spec 15.3).
func (t *Tracker) resolve(ctx context.Context, f repository.Finding, status FeedbackStatus, reason string) {
	updated, err := t.repo.UpdateFindingFeedbackStatus(ctx, f.ID, string(status))
	if err != nil {
		t.logger.Sugar().Warnw("feedback: status update failed",
			"finding", f.ID, "status", status, "error", err)
		return
	}
	if updated {
		t.logger.Sugar().Infow("feedback: finding resolved",
			"finding", f.ID, "status", status, "reason", reason)
	}
}

// windowLines returns the up-to window lines on each side of line
// (1-based, inclusive), i.e. the local context of the finding (FR-42).
func windowLines(content string, line, window int) string {
	if line <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	start := line - window
	if start < 1 {
		start = 1
	}
	end := line + window
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// splitRepo splits "owner/repo".
func splitRepo(full string) (owner, name string, ok bool) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
