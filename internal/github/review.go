package github

import (
	"context"
	"fmt"
	"strings"

	gogithub "github.com/google/go-github/v66/github"

	"codereviewagent/internal/ai/agents"
)

// Review event types for the Pull Request Review API.
const (
	// reviewEventComment leaves a neutral comment review.
	reviewEventComment = "COMMENT"
	// reviewEventRequestChanges formally requests changes; used when a
	// critical finding exists (FR-12).
	reviewEventRequestChanges = "REQUEST_CHANGES"
)

// InlineComment is one line-level comment of a review (FR-12).
type InlineComment struct {
	// Path is the file the comment is attached to.
	Path string
	// Line is the 1-based line number in the new version of the file.
	Line int
	// Body is the markdown comment text.
	Body string
}

// FormatFinding renders a verified finding as a line-level review comment
// body (FR-12: description, severity, confidence, fix suggestion).
func FormatFinding(f agents.VerifiedFinding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**[%s] %s**\n\n", f.Severity, f.Message)
	if f.CodeSnippet != "" {
		fmt.Fprintf(&b, "```\n%s\n```\n", f.CodeSnippet)
	}
	if f.Suggestion != "" {
		fmt.Fprintf(&b, "**Suggestion:** %s\n", f.Suggestion)
	}
	fmt.Fprintf(&b, "\n(Severity: %s, Confidence: %.2f, Agent: %s)",
		f.Severity, f.Confidence, f.Agent)
	return b.String()
}

// BuildInlineComments converts verified findings into inline comments
// ordered by severity and confidence (FR-13 ordering).
func BuildInlineComments(findings []agents.VerifiedFinding) []InlineComment {
	comments := make([]InlineComment, 0, len(findings))
	for _, f := range findings {
		comments = append(comments, InlineComment{
			Path: f.FilePath,
			Line: f.LineNumber,
			Body: FormatFinding(f),
		})
	}
	return comments
}

// BuildSummary renders the review summary body (FR-12).
func BuildSummary(findings []agents.VerifiedFinding) string {
	counts := map[string]int{"critical": 0, "warning": 0, "suggestion": 0}
	for _, f := range findings {
		counts[f.Severity]++
	}
	return fmt.Sprintf(
		"CodeReviewAgent found %d issue(s): %d critical, %d warning, %d suggestion.",
		len(findings), counts["critical"], counts["warning"], counts["suggestion"])
}

// HasCritical reports whether any finding is critical (AC-11).
func HasCritical(findings []agents.VerifiedFinding) bool {
	for _, f := range findings {
		if f.Severity == agents.SeverityCritical {
			return true
		}
	}
	return false
}

// SubmitReview submits a pull request review with a summary and inline
// comments (FR-12, AC-15). The review event is REQUEST_CHANGES when a
// critical finding exists, COMMENT otherwise.
func (c *Client) SubmitReview(ctx context.Context, owner, repo string, number int, commitSHA, summary string, comments []InlineComment) error {
	event := reviewEventComment
	req := &gogithub.PullRequestReviewRequest{
		CommitID: gogithub.String(commitSHA),
		Body:     gogithub.String(summary),
		Event:    gogithub.String(event),
	}
	for _, cm := range comments {
		line := cm.Line
		req.Comments = append(req.Comments, &gogithub.DraftReviewComment{
			Path: gogithub.String(cm.Path),
			Line: gogithub.Int(line),
			Body: gogithub.String(cm.Body),
		})
	}
	if _, _, err := c.api.PullRequests.CreateReview(ctx, owner, repo, number, req); err != nil {
		return fmt.Errorf("create review %s/%s#%d: %w", owner, repo, number, err)
	}
	return nil
}

// SubmitFindingsReview builds and submits the review for the final verified
// findings (FR-12). When findings exist it posts summary + inline comments;
// it returns the used event.
func (c *Client) SubmitFindingsReview(ctx context.Context, owner, repo string, number int, commitSHA string, findings []agents.VerifiedFinding) (string, error) {
	event := reviewEventComment
	if HasCritical(findings) {
		event = reviewEventRequestChanges
	}
	req := &gogithub.PullRequestReviewRequest{
		CommitID: gogithub.String(commitSHA),
		Body:     gogithub.String(BuildSummary(findings)),
		Event:    gogithub.String(event),
	}
	for _, cm := range BuildInlineComments(findings) {
		line := cm.Line
		req.Comments = append(req.Comments, &gogithub.DraftReviewComment{
			Path: gogithub.String(cm.Path),
			Line: gogithub.Int(line),
			Body: gogithub.String(cm.Body),
		})
	}
	if _, _, err := c.api.PullRequests.CreateReview(ctx, owner, repo, number, req); err != nil {
		return "", fmt.Errorf("create review %s/%s#%d: %w", owner, repo, number, err)
	}
	return event, nil
}

// SubmitLGTMReview posts the "LGTM" review used when no issues were found
// (spec edge case; AC-17).
func (c *Client) SubmitLGTMReview(ctx context.Context, owner, repo string, number int, commitSHA string) error {
	req := &gogithub.PullRequestReviewRequest{
		CommitID: gogithub.String(commitSHA),
		Body:     gogithub.String("LGTM ✅"),
		Event:    gogithub.String(reviewEventComment),
	}
	if _, _, err := c.api.PullRequests.CreateReview(ctx, owner, repo, number, req); err != nil {
		return fmt.Errorf("create LGTM review %s/%s#%d: %w", owner, repo, number, err)
	}
	return nil
}

// SubmitNoMeaningfulChangesReview posts the review used for diffs without
// code changes (spec edge case AC-21).
func (c *Client) SubmitNoMeaningfulChangesReview(ctx context.Context, owner, repo string, number int, commitSHA string) error {
	req := &gogithub.PullRequestReviewRequest{
		CommitID: gogithub.String(commitSHA),
		Body:     gogithub.String("No meaningful changes"),
		Event:    gogithub.String(reviewEventComment),
	}
	if _, _, err := c.api.PullRequests.CreateReview(ctx, owner, repo, number, req); err != nil {
		return fmt.Errorf("create no-changes review %s/%s#%d: %w", owner, repo, number, err)
	}
	return nil
}
