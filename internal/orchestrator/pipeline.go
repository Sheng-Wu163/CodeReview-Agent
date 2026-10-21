package orchestrator

import (
	"context"
	"encoding/json"
	"strings"

	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
	"codereviewagent/internal/analysis"
	"codereviewagent/internal/github"
	"codereviewagent/internal/repository"
	"codereviewagent/internal/vcs"
)

// GitHubAPI is the GitHub surface the pipeline needs; implemented by
// github.Client, mocked in tests. Defined in the consumer per convention.
type GitHubAPI interface {
	GetPRDiff(ctx context.Context, owner, repo string, number int) (string, error)
	SubmitFindingsReview(ctx context.Context, owner, repo string, number int, commitSHA string, findings []agents.VerifiedFinding) (string, error)
	// SubmitFindingsReviewWithWarnings posts the findings review with the
	// v1.8 Harness warnings appended to the summary (FR-49 / AC-106).
	SubmitFindingsReviewWithWarnings(ctx context.Context, owner, repo string, number int, commitSHA string, findings []agents.VerifiedFinding, warnings []string) (string, error)
	SubmitLGTMReview(ctx context.Context, owner, repo string, number int, commitSHA string) error
	SubmitNoMeaningfulChangesReview(ctx context.Context, owner, repo string, number int, commitSHA string) error
	UpdateCommitStatus(ctx context.Context, owner, repo, sha, state, description string) error
}

// ReviewStore is the persistence surface the pipeline needs; implemented by
// repository.ReviewRepo. Defined in the consumer per convention.
type ReviewStore interface {
	CreateReview(ctx context.Context, rev *repository.Review) (int64, error)
	UpdateReviewStatus(ctx context.Context, id int64, status, summary *string) error
	// UpdateReviewPreFindings persists the serialized static-analysis
	// preFindings JSON (v1.3 FR-24 / AC-43).
	UpdateReviewPreFindings(ctx context.Context, id int64, preFindings string) error
	// UpdateReviewAgentStatus persists the per-agent outcome map of the
	// adaptive strategy (v1.2 FR-37 / AC-77).
	UpdateReviewAgentStatus(ctx context.Context, id int64, status map[string]string) error
	// UpdateReviewAgentResults persists the serialized structured Harness
	// results of every executed agent (v1.8 FR-49 / 17.4 / T52).
	UpdateReviewAgentResults(ctx context.Context, id int64, results string) error
	CreateFinding(ctx context.Context, f *repository.Finding) (int64, error)
}

// EventInput is one pull_request webhook event to process (FR-02).
type EventInput struct {
	RepoFullName string
	PRNumber     int
	HeadSHA      string
}

// Pipeline connects the webhook event to the full review flow:
// clone workspace -> orchestrator.Run -> persist -> GitHub review + status
// (FR-12, FR-14, AC-03).
type Pipeline struct {
	orch   *Orchestrator
	gh     GitHubAPI
	store  ReviewStore
	token  string
	logger *zap.Logger
	// cloneURL overrides the GitHub clone URL (tests).
	cloneURL string
}

// NewPipeline creates the end-to-end event pipeline.
func NewPipeline(orch *Orchestrator, gh GitHubAPI, store ReviewStore, token string, logger *zap.Logger) *Pipeline {
	return &Pipeline{orch: orch, gh: gh, store: store, token: token, logger: logger}
}

// SetCloneURL overrides the clone URL (used by tests).
func (p *Pipeline) SetCloneURL(url string) { p.cloneURL = url }

// Process handles one pull_request event (opened/synchronize): it never
// returns an error to the webhook layer - every failure is logged and
// reflected on the commit status (NFR-02).
func (p *Pipeline) Process(ctx context.Context, ev EventInput) {
	log := p.logger.Sugar().With("repo", ev.RepoFullName, "pr", ev.PRNumber, "head_sha", ev.HeadSHA)

	owner, repoName, ok := splitRepo(ev.RepoFullName)
	if !ok {
		log.Errorw("invalid repository full name", "repo", ev.RepoFullName)
		return
	}

	// Persist the review record up-front so the dashboard shows in-progress
	// runs (FR-14).
	reviewID, err := p.store.CreateReview(ctx, &repository.Review{
		RepoFullName: ev.RepoFullName,
		PRNumber:     ev.PRNumber,
		HeadSHA:      ev.HeadSHA,
		Status:       "pending",
	})
	if err != nil {
		log.Errorw("create review record", "error", err)
		return
	}

	// Clone the PR code for the review tools (FR-11).
	root, cleanup, err := vcs.PrepareWorkspace(ctx, p.token, p.cloneURL, owner, repoName, ev.PRNumber, ev.HeadSHA)
	if err != nil {
		log.Errorw("prepare workspace", "error", err)
		p.failReview(ctx, reviewID, owner, repoName, ev, "failed to prepare workspace")
		return
	}
	defer cleanup()

	// Fetch the diff (FR-03 input).
	diffText, err := p.gh.GetPRDiff(ctx, owner, repoName, ev.PRNumber)
	if err != nil {
		log.Errorw("fetch PR diff", "error", err)
		p.failReview(ctx, reviewID, owner, repoName, ev, "failed to fetch diff")
		return
	}

	// Run the full review pipeline.
	res, err := p.orch.Run(ctx, Input{
		RepoFullName:  ev.RepoFullName,
		PRNumber:      ev.PRNumber,
		HeadSHA:       ev.HeadSHA,
		DiffText:      diffText,
		WorkspaceRoot: root,
	})
	if err != nil {
		log.Errorw("review pipeline failed", "error", err)
		p.failReview(ctx, reviewID, owner, repoName, ev, err.Error())
		return
	}

	// Persist the static-analysis preFindings for later analysis (AC-43).
	if len(res.PreFindings) > 0 {
		preJSON, err := analysis.MarshalPreFindings(res.PreFindings)
		if err != nil {
			log.Errorw("marshal preFindings", "error", err)
		} else if err := p.store.UpdateReviewPreFindings(ctx, reviewID, preJSON); err != nil {
			log.Errorw("persist preFindings", "error", err)
		}
	}

	// Persist the adaptive-strategy agent outcomes, including the
	// "skipped_by_strategy" marks (v1.2 FR-37 / AC-77).
	if len(res.AgentStatus) > 0 {
		if err := p.store.UpdateReviewAgentStatus(ctx, reviewID, res.AgentStatus); err != nil {
			log.Errorw("persist agent status", "error", err)
		}
	}

	// Persist the structured Harness results, including failed agents and
	// their tool-call fingerprints (v1.8 FR-49 / 17.4 / T52).
	if len(res.AgentResults) > 0 {
		data, err := json.Marshal(res.AgentResults)
		if err != nil {
			log.Errorw("marshal agent results", "error", err)
		} else if err := p.store.UpdateReviewAgentResults(ctx, reviewID, string(data)); err != nil {
			log.Errorw("persist agent results", "error", err)
		}
	}

	// Persist findings (FR-14).
	for _, f := range res.Findings {
		if _, err := p.store.CreateFinding(ctx, &repository.Finding{
			ReviewID:   reviewID,
			Agent:      f.Agent,
			Severity:   f.Severity,
			FilePath:   f.FilePath,
			LineNumber: f.LineNumber,
			Message:    f.Message,
			Suggestion: f.Suggestion,
			Confidence: f.Confidence,
		}); err != nil {
			log.Errorw("persist finding", "error", err)
		}
	}

	// GitHub Review (FR-12 / AC-15 / AC-17 / AC-21). Failed and partial
	// Harness outcomes are surfaced in the review body (v1.8 FR-49 /
	// AC-106).
	warnings := res.AgentWarnings()
	switch {
	case res.NoMeaningfulChanges:
		err = p.gh.SubmitNoMeaningfulChangesReview(ctx, owner, repoName, ev.PRNumber, ev.HeadSHA)
	case len(warnings) > 0:
		_, err = p.gh.SubmitFindingsReviewWithWarnings(ctx, owner, repoName, ev.PRNumber, ev.HeadSHA, res.Findings, warnings)
	case len(res.Findings) == 0:
		err = p.gh.SubmitLGTMReview(ctx, owner, repoName, ev.PRNumber, ev.HeadSHA)
	default:
		_, err = p.gh.SubmitFindingsReview(ctx, owner, repoName, ev.PRNumber, ev.HeadSHA, res.Findings)
	}
	if err != nil {
		log.Errorw("submit PR review", "error", err)
	}

	// Commit status (FR-12 / AC-11 / AC-17).
	state, description := github.StatusForFindings(res.Findings)
	if err := p.gh.UpdateCommitStatus(ctx, owner, repoName, ev.HeadSHA, state, description); err != nil {
		log.Errorw("update commit status", "error", err)
	}

	// Finalize the review record.
	finalStatus := "completed"
	summary := github.BuildSummary(res.Findings)
	if err := p.store.UpdateReviewStatus(ctx, reviewID, &finalStatus, &summary); err != nil {
		log.Errorw("update review status", "error", err)
	}
	log.Infow("pull request processed",
		"findings", len(res.Findings),
		"agent_errors", len(res.AgentErrors),
		"status", finalStatus)
}

// failReview marks a review as failed and sets a failing commit status so
// the developer sees why no review was produced.
func (p *Pipeline) failReview(ctx context.Context, reviewID int64, owner, repoName string, ev EventInput, reason string) {
	status := "failed"
	if err := p.store.UpdateReviewStatus(ctx, reviewID, &status, &reason); err != nil {
		p.logger.Sugar().Errorw("update review status", "error", err)
	}
	if err := p.gh.UpdateCommitStatus(ctx, owner, repoName, ev.HeadSHA, github.StatusFailure,
		"CodeReviewAgent failed: "+reason); err != nil {
		p.logger.Sugar().Errorw("update commit status", "error", err)
	}
}

// splitRepo splits "owner/repo".
func splitRepo(full string) (owner, name string, ok bool) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
