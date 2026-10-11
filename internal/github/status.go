package github

import (
	"context"
	"fmt"

	gogithub "github.com/google/go-github/v66/github"

	"codereviewagent/internal/ai/agents"
)

// Commit status states (FR-12).
const (
	// StatusSuccess marks a green check.
	StatusSuccess = "success"
	// StatusFailure marks a red cross (critical findings, AC-11).
	StatusFailure = "failure"
	// StatusPending marks an in-progress run.
	StatusPending = "pending"
)

// statusContext is the GitHub status context shown next to the commit.
const statusContext = "codereviewagent"

// UpdateCommitStatus sets the commit status of the reviewed head SHA
// (FR-12, AC-11/AC-17).
func (c *Client) UpdateCommitStatus(ctx context.Context, owner, repo, sha, state, description string) error {
	status := &gogithub.RepoStatus{
		State:       gogithub.String(state),
		Context:     gogithub.String(statusContext),
		Description: gogithub.String(description),
	}
	if _, _, err := c.api.Repositories.CreateStatus(ctx, owner, repo, sha, status); err != nil {
		return fmt.Errorf("update commit status %s/%s@%s: %w", owner, repo, sha, err)
	}
	return nil
}

// StatusForFindings maps the final findings to a commit status state and
// description (AC-11: critical findings fail the check; AC-17: no findings
// passes it).
func StatusForFindings(findings []agents.VerifiedFinding) (state, description string) {
	if HasCritical(findings) {
		return StatusFailure, fmt.Sprintf("CodeReviewAgent: %d critical finding(s)", countSeverity(findings, agents.SeverityCritical))
	}
	if len(findings) == 0 {
		return StatusSuccess, "CodeReviewAgent: no issues found"
	}
	return StatusSuccess, fmt.Sprintf("CodeReviewAgent: %d non-critical finding(s)", len(findings))
}

// countSeverity counts findings with the given severity.
func countSeverity(findings []agents.VerifiedFinding, severity string) int {
	n := 0
	for _, f := range findings {
		if f.Severity == severity {
			n++
		}
	}
	return n
}
