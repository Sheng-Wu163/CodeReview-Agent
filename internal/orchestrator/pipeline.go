package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
	"codereviewagent/internal/analysis"
	"codereviewagent/internal/github"
	"codereviewagent/internal/repository"
	"codereviewagent/internal/review"
	"codereviewagent/internal/vcs"
)

// GitHubAPI is the GitHub surface the pipeline needs; implemented by
// github.Client, mocked in tests. Defined in the consumer per convention.
type GitHubAPI interface {
	GetPRDiff(ctx context.Context, owner, repo string, number int) (string, error)
	// GetCompareDiff returns base..head for incremental reviews (v1.10).
	GetCompareDiff(ctx context.Context, owner, repo, base, head string) (string, error)
	// ListReviewThreads reads existing review threads (v1.11 FR-67).
	ListReviewThreads(ctx context.Context, owner, repo string, number int) ([]review.ThreadState, error)
	// ResolveReviewThread closes a fixed thread (v1.11 FR-68).
	ResolveReviewThread(ctx context.Context, threadID string) error
	// SubmitReviewPlan publishes only new findings and applies the
	// unresolved-critical event decision (v1.11 FR-67/FR-69).
	SubmitReviewPlan(ctx context.Context, owner, repo string, number int, commitSHA string, plan review.PublishPlan, warnings []string) (string, error)
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
	// UpdateReviewAnchor persists the v1.10 scope/base anchor.
	UpdateReviewAnchor(ctx context.Context, id int64, reviewedSHA, baseSHA, scope, scopeReason string) error
	// LatestCompletedReview finds the previous completed anchor.
	LatestCompletedReview(ctx context.Context, repo string, prNumber int) (*repository.Review, error)
	// ListFindings loads the anchor review's findings.
	ListFindings(ctx context.Context, reviewID int64) ([]repository.Finding, error)
	CreateFinding(ctx context.Context, f *repository.Finding) (int64, error)
}

// EventInput is one pull_request webhook event to process (FR-02).
type EventInput struct {
	RepoFullName string
	PRNumber     int
	HeadSHA      string
	// BeforeSHA is the synchronize event's previous SHA; empty for opened
	// events (v1.10 FR-61).
	BeforeSHA string
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

	// v1.11: read existing threads before the run. Failure degrades to no
	// deduplication instead of aborting the review (AC-131), and D4 uses the
	// resolved threads to filter PRIOR_FINDINGS.
	threads, threadErr := p.gh.ListReviewThreads(ctx, owner, repoName, ev.PRNumber)
	if threadErr != nil {
		log.Warnw("review thread read failed, publishing without deduplication", "error", threadErr)
		threads = nil
	}

	// v1.10: resolve the previous completed anchor and choose the diff
	// scope (FR-61/FR-62).
	anchor, err := p.store.LatestCompletedReview(ctx, ev.RepoFullName, ev.PRNumber)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Warnw("latest completed review lookup failed", "error", err)
		}
		anchor = nil
	}

	diffText, scope, baseSHA, scopeReason, err := p.resolveDiff(ctx, owner, repoName, ev, anchor)
	if err != nil {
		log.Errorw("fetch review diff", "error", err)
		p.failReview(ctx, reviewID, owner, repoName, ev, "failed to fetch diff")
		return
	}
	if err := p.store.UpdateReviewAnchor(ctx, reviewID, ev.HeadSHA, baseSHA, string(scope), scopeReason); err != nil {
		log.Errorw("persist review anchor", "error", err)
	}

	priorFindings := p.loadPriorFindings(ctx, anchor, threads)

	// Run the review pipeline.
	res, err := p.orch.Run(ctx, Input{
		RepoFullName:  ev.RepoFullName,
		PRNumber:      ev.PRNumber,
		HeadSHA:       ev.HeadSHA,
		DiffText:      diffText,
		WorkspaceRoot: root,
		Scope:         scope,
		ScopeReason:   scopeReason,
		PriorFindings: priorFindings,
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
		fingerprint := f.PriorFingerprint
		if fingerprint == "" {
			fingerprint = review.FindingFingerprint(f.FilePath, f.Agent, f.Message)
		}
		if _, err := p.store.CreateFinding(ctx, &repository.Finding{
			ReviewID:    reviewID,
			Agent:       f.Agent,
			Severity:    f.Severity,
			FilePath:    f.FilePath,
			LineNumber:  f.LineNumber,
			Message:     f.Message,
			Suggestion:  f.Suggestion,
			Confidence:  f.Confidence,
			Fingerprint: fingerprint,
		}); err != nil {
			log.Errorw("persist finding", "error", err)
		}
	}

	// GitHub Review (FR-12 / AC-15 / AC-17 / AC-21). Failed and partial
	// Harness outcomes are surfaced in the review body (v1.8 FR-49 /
	// AC-106).
	warnings := res.AgentWarnings()
	if threadErr != nil {
		warnings = append(warnings, "⚠️ 线程读取失败，可能存在重复评论")
	}

	// v1.11: classify findings against existing threads. Only plan.New is
	// submitted inline; persisting findings are counted, and fixed threads
	// are resolved below.
	plan := review.PlanPublish(res.Findings, threads, res.PriorVerdicts)
	switch {
	case res.NoMeaningfulChanges:
		err = p.gh.SubmitNoMeaningfulChangesReview(ctx, owner, repoName, ev.PRNumber, ev.HeadSHA)
	case threadErr == nil && len(plan.New) == 0 && len(plan.Persisting) == 0 &&
		len(plan.Resolved) == 0 && len(warnings) == 0:
		err = p.gh.SubmitLGTMReview(ctx, owner, repoName, ev.PRNumber, ev.HeadSHA)
	default:
		_, err = p.gh.SubmitReviewPlan(ctx, owner, repoName, ev.PRNumber, ev.HeadSHA, plan, warnings)
	}
	if err != nil {
		log.Errorw("submit PR review", "error", err)
	}

	// v1.11 FR-68: resolve only threads whose PRIOR_FINDING was judged
	// fixed. Failures are logged and never block the review.
	for _, thread := range plan.Resolved {
		if res.PriorVerdicts[thread.Fingerprint] != agents.VerdictFixed {
			continue
		}
		if err := p.gh.ResolveReviewThread(ctx, thread.ThreadID); err != nil {
			log.Warnw("resolve review thread failed", "thread", thread.ThreadID, "error", err)
		}
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

// resolveDiff selects the v1.10 review base and diff (FR-61). The
// reviewed_sha anchor takes priority over the webhook before SHA, covering
// reviews skipped by failed runs or lost webhooks.
func (p *Pipeline) resolveDiff(ctx context.Context, owner, repo string, ev EventInput, anchor *repository.Review) (string, ReviewScope, string, string, error) {
	base := ev.BeforeSHA
	if anchor != nil && anchor.ReviewedSHA != "" {
		base = anchor.ReviewedSHA
	}

	// No usable base, or a re-run of the same commit: review the full PR.
	if base == "" || base == ev.HeadSHA {
		diffText, err := p.gh.GetPRDiff(ctx, owner, repo, ev.PRNumber)
		return diffText, ScopeFull, "", "no_base", err
	}

	diffText, err := p.gh.GetCompareDiff(ctx, owner, repo, base, ev.HeadSHA)
	if err == nil {
		return diffText, ScopeIncremental, base, "", nil
	}

	reason := "before_unreachable"
	if errors.Is(err, github.ErrCompareLimit) {
		reason = "compare_limit"
	} else if !errors.Is(err, github.ErrCompareBaseUnreachable) {
		p.logger.Sugar().Warnw("compare API failed, falling back to full review",
			"base", base, "head", ev.HeadSHA, "error", err)
	}
	fullDiff, fullErr := p.gh.GetPRDiff(ctx, owner, repo, ev.PRNumber)
	return fullDiff, ScopeFull, "", reason, fullErr
}

// loadPriorFindings loads the anchor's unresolved findings for PRIOR_FINDINGS
// injection (v1.10 FR-63). accepted_implicit findings and findings whose
// thread a human already resolved are skipped (D4).
func (p *Pipeline) loadPriorFindings(ctx context.Context, anchor *repository.Review, threads []review.ThreadState) []agents.PriorFinding {
	if anchor == nil {
		return nil
	}
	findings, err := p.store.ListFindings(ctx, anchor.ID)
	if err != nil {
		p.logger.Sugar().Warnw("load anchor findings failed", "review", anchor.ID, "error", err)
		return nil
	}

	resolvedFingerprints := make(map[string]bool)
	for _, thread := range threads {
		if thread.IsResolved && thread.Fingerprint != "" {
			resolvedFingerprints[thread.Fingerprint] = true
		}
	}

	prior := make([]agents.PriorFinding, 0, len(findings))
	for _, f := range findings {
		if f.FeedbackStatus == "accepted_implicit" {
			continue
		}
		fingerprint := f.Fingerprint
		if fingerprint == "" {
			fingerprint = review.FindingFingerprint(f.FilePath, f.Agent, f.Message)
		}
		if resolvedFingerprints[fingerprint] {
			continue
		}
		prior = append(prior, agents.PriorFinding{
			Fingerprint: fingerprint,
			Agent:       f.Agent,
			FilePath:    f.FilePath,
			LineNumber:  f.LineNumber,
			Severity:    f.Severity,
			Message:     f.Message,
		})
	}
	return prior
}

// splitRepo splits "owner/repo".
func splitRepo(full string) (owner, name string, ok bool) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
