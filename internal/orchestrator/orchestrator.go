// Package orchestrator coordinates the full review pipeline: diff parsing,
// file filtering, parallel agent execution and adversarial verification
// (FR-02, FR-03, FR-04, FR-09, FR-10).
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	ltools "github.com/tmc/langchaingo/tools"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"codereviewagent/internal/ai/agents"
	"codereviewagent/internal/ai/tools"
	"codereviewagent/internal/analysis"
	"codereviewagent/internal/config"
	ctxcontext "codereviewagent/internal/context"
	"codereviewagent/internal/diff"
)

// duplicateWindow is the time window in which a repeated webhook for the
// same PR head SHA is ignored (spec edge case: 30 seconds).
const duplicateWindow = 30 * time.Second

// ErrDuplicatePR is returned when a PR head SHA triggers another review
// within the deduplication window (AC-25).
var ErrDuplicatePR = errors.New("duplicate PR event within deduplication window, ignored")

// Input describes one review request (FR-02).
type Input struct {
	// RepoFullName is the repository, e.g. "owner/repo".
	RepoFullName string
	// PRNumber is the pull request number.
	PRNumber int
	// HeadSHA is the head commit SHA of the pull request.
	HeadSHA string
	// DiffText is the raw unified diff of the pull request.
	DiffText string
	// WorkspaceRoot is the checked-out workspace the tools operate on.
	// When empty the orchestrator's default root is used.
	WorkspaceRoot string
}

// Verifier is the verification step consumed by the orchestrator. The
// concrete agents.Verifier implements it; tests may mock it. Per the
// project convention this interface is defined in the consumer package.
type Verifier interface {
	Verify(ctx context.Context, findings []agents.AgentFinding, input agents.ReviewInput) ([]agents.VerifiedFinding, error)
}

// VerifierFactory creates a Verifier bound to the given workspace root
// (the read_file tool used for hallucination filtering needs the root).
type VerifierFactory func(root string) Verifier

// AnalysisRunner is the pre-review static analysis pipeline consumed by the
// orchestrator (FR-17); analysis.Pipeline implements it. Defined in the
// consumer per project convention.
type AnalysisRunner interface {
	Run(ctx context.Context, files []diff.FileDiff) (*analysis.Result, error)
}

// AnalysisRunnerFactory creates an AnalysisRunner bound to a workspace root.
type AnalysisRunnerFactory func(root string) AnalysisRunner

// ContextBuilder builds the local context bundle injected into the agents
// (v1.1 FR-25..FR-29); context.Builder implements it. Defined in the
// consumer per project convention.
type ContextBuilder interface {
	Build(ctx context.Context, diffs []diff.FileDiff) (ctxcontext.ContextBundle, error)
}

// ContextBuilderFactory creates a ContextBuilder bound to a workspace root.
type ContextBuilderFactory func(root string) ContextBuilder

// Result carries everything the caller needs after one review run.
type Result struct {
	// Input echoes the review request.
	Input Input
	// ReviewedFiles lists the file paths that entered the review queue.
	ReviewedFiles []string
	// SkippedFiles lists the file paths excluded by filtering (with reason
	// prefix).
	SkippedFiles []string
	// Findings holds the verified, severity-sorted final findings.
	Findings []agents.VerifiedFinding
	// PreFindings are the raw static-analysis findings injected into the
	// agents (v1.3); the caller may persist them for later analysis.
	PreFindings []analysis.PreAnalysisFinding
	// AgentErrors records per-agent failures (FR-09 / NFR-02: an agent
	// failure must not fail the whole review).
	AgentErrors map[string]string
	// NoMeaningfulChanges is true when the diff contained no code changes
	// (spec edge case: comment-only or whitespace-only diffs).
	NoMeaningfulChanges bool
	// Duration is the total wall time of the run (NFR-04 observability).
	Duration time.Duration
}

// Orchestrator wires the pipeline stages together.
type Orchestrator struct {
	logger         *zap.Logger
	root           string
	agents         []agents.ReviewAgent
	verifierFactor VerifierFactory
	// analysisFactory creates the pre-review static analysis runner for a
	// workspace root (FR-17). May be nil to skip pre-analysis.
	analysisFactory AnalysisRunnerFactory
	// contextFactory creates the local-context builder for a workspace root
	// (v1.1 FR-25). May be nil to skip context injection.
	contextFactory ContextBuilderFactory
	// staticCfg is the static analysis configuration applied by
	// analysisFactory (set via SetAnalysisConfig).
	staticCfg config.StaticAnalysisConfig
	filter    *diff.Filter
	// agentTimeout bounds each agent's ReAct loop (AC-22: LLM API calls
	// exceeding 60s mark that agent as failed without affecting others).
	agentTimeout time.Duration

	mu       sync.Mutex
	lastSeen map[string]time.Time // head SHA -> last trigger time (AC-25)
}

// New creates an Orchestrator. root is the default review workspace the
// tools operate on; reviewAgents are executed in parallel; verifierFactory
// may be nil to skip verification; analysisFactory may be nil to skip
// static pre-analysis (both useful in tests).
func New(logger *zap.Logger, root string, reviewAgents []agents.ReviewAgent, verifierFactory VerifierFactory, filter *diff.Filter) *Orchestrator {
	if filter == nil {
		filter = diff.NewFilter(diff.DefaultFilterRules())
	}
	o := &Orchestrator{
		logger:         logger,
		root:           root,
		agents:         reviewAgents,
		verifierFactor: verifierFactory,
		filter:         filter,
		agentTimeout:   60 * time.Second,
		lastSeen:       make(map[string]time.Time),
	}
	o.analysisFactory = func(wsRoot string) AnalysisRunner {
		return analysis.NewPipeline(logger, wsRoot, o.staticCfg)
	}
	o.contextFactory = func(wsRoot string) ContextBuilder {
		return ctxcontext.NewBuilder(wsRoot, logger)
	}
	return o
}

// SetContextBuilderFactory overrides the local-context builder factory
// (v1.1); tests inject a mock, production uses the default Builder.
func (o *Orchestrator) SetContextBuilderFactory(f ContextBuilderFactory) {
	o.contextFactory = f
}

// SetAnalysisConfig enables the static analysis pipeline (FR-17) with the
// given configuration. Must be called before Run.
func (o *Orchestrator) SetAnalysisConfig(cfg config.StaticAnalysisConfig) {
	o.staticCfg = cfg
}

// severityRank orders findings critical > warning > suggestion > unknown.
var severityRank = map[string]int{
	agents.SeverityCritical:   0,
	agents.SeverityWarning:    1,
	agents.SeveritySuggestion: 2,
}

// Run executes the full pipeline for one PR event (T12 steps 1-7).
func (o *Orchestrator) Run(ctx context.Context, in Input) (*Result, error) {
	if err := o.checkDuplicate(in.HeadSHA); err != nil {
		return nil, err
	}
	start := time.Now()
	res := &Result{Input: in, AgentErrors: make(map[string]string)}

	// Step 2: parse the diff.
	files, err := diff.Parse(in.DiffText)
	if err != nil {
		return nil, fmt.Errorf("parse diff: %w", err)
	}

	// Step 3: filter files (FR-04) and skip binaries (AC-24).
	reviewable := make([]diff.FileDiff, 0, len(files))
	for _, f := range files {
		p := f.Path
		if p == "" {
			p = f.OldPath
		}
		if f.IsBinary {
			res.SkippedFiles = append(res.SkippedFiles, p+" (binary)")
			o.logger.Sugar().Infow("skipped binary file", "path", p)
			continue
		}
		if !o.filter.ShouldInclude(p) {
			res.SkippedFiles = append(res.SkippedFiles, p+" (filtered)")
			continue
		}
		reviewable = append(reviewable, f)
	}

	// Meaningful-change gate: comment-only diffs skip the whole review.
	hasChanges := false
	for _, f := range reviewable {
		if f.HasChanges() {
			hasChanges = true
			break
		}
	}
	if !hasChanges {
		res.NoMeaningfulChanges = true
		res.Duration = time.Since(start)
		o.logger.Sugar().Infow("no meaningful changes, skipping review",
			"repo", in.RepoFullName, "pr", in.PRNumber)
		return res, nil
	}
	for _, f := range reviewable {
		p := f.Path
		if p == "" {
			p = f.OldPath
		}
		res.ReviewedFiles = append(res.ReviewedFiles, p)
	}

	// Step 3.5: static analysis pre-run (FR-17): deterministic tools run
	// before the agents; their findings are injected into agent prompts.
	root := in.WorkspaceRoot
	if root == "" {
		root = o.root
	}
	var preFindings []analysis.PreAnalysisFinding
	if o.analysisFactory != nil {
		analysisRes, err := o.analysisFactory(root).Run(ctx, reviewable)
		if err != nil {
			// All static analysis tools unavailable: agents degrade to pure
			// LLM review, the pipeline continues (spec 11.5).
			o.logger.Sugar().Errorw("static analysis pipeline failed, degrading to LLM-only",
				"error", err)
		} else {
			for _, f := range reviewable {
				preFindings = append(preFindings, analysisRes.ByFile[f.Path]...)
			}
			for _, f := range analysisRes.Failures {
				res.AgentErrors["static-analysis/"+f.Tool] = f.Err
				o.logger.Sugar().Warnw("static analyzer degraded",
					"tool", f.Tool, "file", f.File, "error", f.Err)
			}
			o.logger.Sugar().Infow("static analysis completed",
				"findings", len(preFindings), "elapsed", analysisRes.Duration)
			res.PreFindings = preFindings
		}
	}

	// Step 4+5: run the review agents in parallel; collect findings and
	// per-agent failures (FR-09 / NFR-02).
	agentTools := o.buildTools(root)

	// Step 3.6: local context injection (v1.1 FR-25..FR-29). The builder
	// degrades to an empty bundle on failure, so this never blocks review.
	var ctxBundle ctxcontext.ContextBundle
	if o.contextFactory != nil {
		bundle, err := o.contextFactory(root).Build(ctx, reviewable)
		if err != nil {
			o.logger.Sugar().Warnw("local context build failed, continuing without context",
				"error", err)
		} else {
			ctxBundle = bundle
			if !bundle.IsEmpty() {
				o.logger.Sugar().Infow("local context built",
					"changes", len(bundle.Changes),
					"callers", len(bundle.Callers),
					"callees", len(bundle.Callees))
			}
		}
	}

	agentInput := agents.ReviewInput{
		Diffs:       reviewable,
		PreFindings: preFindings,
		Context:     ctxBundle,
		Tools:       agentTools,
	}
	var mu sync.Mutex
	rawFindings := make([]agents.AgentFinding, 0)
	g := new(errgroup.Group)
	for _, a := range o.agents {
		agent := a
		g.Go(func() error {
			agentStart := time.Now()
			fnd, err := o.runAgent(ctx, agent, agentInput)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.AgentErrors[agent.Name()] = err.Error()
				o.logger.Sugar().Errorw("review agent failed",
					"agent", agent.Name(), "error", err,
					"elapsed", time.Since(agentStart))
				return nil // do not fail the group
			}
			rawFindings = append(rawFindings, fnd...)
			o.logger.Sugar().Infow("review agent finished",
				"agent", agent.Name(),
				"findings", len(fnd),
				"elapsed", time.Since(agentStart))
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("agent group: %w", err)
	}

	// Step 6: adversarial verification (FR-10). The verifier is built per
	// run because its read_file tool is bound to this run's workspace.
	var verifier Verifier
	if o.verifierFactor != nil {
		verifier = o.verifierFactor(root)
	}
	if verifier != nil && len(rawFindings) > 0 {
		verified, err := verifier.Verify(ctx, rawFindings, agentInput)
		if err != nil {
			return nil, fmt.Errorf("verify findings: %w", err)
		}
		res.Findings = verified
	}

	// Step 7: severity + confidence ordering (FR-12, FR-13).
	sortFindings(res.Findings)
	res.Duration = time.Since(start)
	o.logger.Sugar().Infow("review completed",
		"repo", in.RepoFullName, "pr", in.PRNumber,
		"head_sha", in.HeadSHA,
		"final_findings", len(res.Findings),
		"agent_errors", len(res.AgentErrors),
		"duration", res.Duration)
	return res, nil
}

// runAgent executes one agent under its own timeout (AC-22).
func (o *Orchestrator) runAgent(ctx context.Context, a agents.ReviewAgent, input agents.ReviewInput) ([]agents.AgentFinding, error) {
	runCtx, cancel := context.WithTimeout(ctx, o.agentTimeout)
	defer cancel()
	return a.Review(runCtx, input)
}

// buildTools creates the context-collection tool set for the review agents,
// bound to the given review workspace (FR-11). run_static_analysis was
// removed in v1.3 (FR-23): static analysis runs in the pre-analysis
// pipeline, not through agent tools.
func (o *Orchestrator) buildTools(root string) []ltools.Tool {
	return []ltools.Tool{
		tools.ReadFileTool{Root: root},
	}
}

// checkDuplicate ignores repeated triggers for the same head SHA inside the
// deduplication window (AC-25).
func (o *Orchestrator) checkDuplicate(headSHA string) error {
	if headSHA == "" {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	if last, ok := o.lastSeen[headSHA]; ok && now.Sub(last) < duplicateWindow {
		return ErrDuplicatePR
	}
	o.lastSeen[headSHA] = now
	return nil
}

// sortFindings orders findings by severity rank then confidence descending.
func sortFindings(findings []agents.VerifiedFinding) {
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := severityRank[findings[i].Severity], severityRank[findings[j].Severity]
		if ri != rj {
			return ri < rj
		}
		return findings[i].Confidence > findings[j].Confidence
	})
}

// SeverityRank exposes the severity ordering used for final sorting.
func SeverityRank(severity string) int {
	return severityRank[strings.ToLower(severity)]
}
