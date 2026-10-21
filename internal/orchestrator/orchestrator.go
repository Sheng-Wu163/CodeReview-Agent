// Package orchestrator coordinates the full review pipeline: diff parsing,
// file filtering, parallel agent execution and adversarial verification
// (FR-02, FR-03, FR-04, FR-09, FR-10).
package orchestrator

import (
	"context"
	"encoding/json"
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
	"codereviewagent/internal/harness"
	"codereviewagent/internal/review"
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

// ComplexityAssessor assesses a PR's complexity (v1.2 FR-34);
// review.Assessor implements it. Defined in the consumer per convention.
type ComplexityAssessor interface {
	Assess(diffs []diff.FileDiff) review.ComplexityLevel
}

// StrategySelector selects the review strategy for a complexity level
// (v1.2 FR-35); review.Selector implements it.
type StrategySelector interface {
	Select(level review.ComplexityLevel) review.ReviewStrategy
}

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
	// ComplexityLevel is the deterministic complexity assessed for this PR
	// (v1.2 FR-34).
	ComplexityLevel review.ComplexityLevel
	// AgentStatus maps every configured agent to its outcome: "executed",
	// "skipped_by_strategy" (v1.2 FR-37) or "failed".
	AgentStatus map[string]string
	// AgentResults holds the structured Harness outcome of every executed
	// agent (v1.8 FR-49), including tool-call logs.
	AgentResults map[string]harness.AgentResult
	// NoMeaningfulChanges is true when the diff contained no code changes
	// (spec edge case: comment-only or whitespace-only diffs).
	NoMeaningfulChanges bool
	// Duration is the total wall time of the run (NFR-04 observability).
	Duration time.Duration
}

// agentDisplayNames maps agent identifiers to the Chinese labels used in
// user-facing warnings.
var agentDisplayNames = map[string]string{
	"security":    "安全",
	"logic":       "逻辑",
	"performance": "性能",
	"lint":        "规范",
}

// AgentWarnings renders the Harness outcomes of failed and partial agents as
// user-facing warning lines (FR-49, AC-106). The result is deterministic:
// agents are visited in sorted order.
func (r *Result) AgentWarnings() []string {
	if len(r.AgentResults) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.AgentResults))
	for name := range r.AgentResults {
		names = append(names, name)
	}
	sort.Strings(names)

	warnings := make([]string, 0, len(names))
	for _, name := range names {
		result := r.AgentResults[name]
		label := agentDisplayNames[name]
		if label == "" {
			label = name
		}
		switch result.Status {
		case harness.StatusFailed:
			warnings = append(warnings, fmt.Sprintf(
				"⚠️ %s审查未完成（%s: failed - %s）", label, name, result.ErrorReason))
		case harness.StatusPartial:
			warnings = append(warnings, fmt.Sprintf(
				"⚠️ %s审查存疑（%s: partial - %s）", label, name, result.ErrorReason))
		}
	}
	return warnings
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
	// assessor and selector implement the v1.2 adaptive review strategy
	// (FR-34/FR-35). They never call an LLM.
	assessor ComplexityAssessor
	selector StrategySelector
	// events is the optional SSE review-stream sink (v1.7 FR-44). Nil
	// disables event emission.
	events chan<- ReviewEvent
	// staticCfg is the static analysis configuration applied by
	// analysisFactory (set via SetAnalysisConfig).
	staticCfg config.StaticAnalysisConfig
	filter    *diff.Filter
	// agentTimeout bounds each agent's ReAct loop (AC-22: LLM API calls
	// exceeding 60s mark that agent as failed without affecting others).
	agentTimeout time.Duration
	// runner is the v1.8 Harness control layer wrapping every agent run
	// (FR-48).
	runner harness.Harness

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
	o.assessor = review.NewAssessor(review.AssessorOptions{})
	o.selector = review.NewSelector()
	o.runner = harness.New(logger, harness.WithTimeout(o.agentTimeout))
	return o
}

// SetHarness overrides the v1.8 Harness control layer. Tests inject a mock;
// production uses the default Executor. Must be called before Run.
func (o *Orchestrator) SetHarness(h harness.Harness) {
	if h != nil {
		o.runner = h
	}
}

// SetReviewStrategy overrides the adaptive review strategy components
// (v1.2). Production wires config-driven thresholds; tests inject mocks.
func (o *Orchestrator) SetReviewStrategy(assessor ComplexityAssessor, selector StrategySelector) {
	if assessor != nil {
		o.assessor = assessor
	}
	if selector != nil {
		o.selector = selector
	}
}

// selectStrategy assesses the diffs and selects the review strategy,
// falling back to the deterministic defaults.
func (o *Orchestrator) selectStrategy(diffs []diff.FileDiff) review.ReviewStrategy {
	assessor := o.assessor
	if assessor == nil {
		assessor = review.NewAssessor(review.AssessorOptions{})
	}
	selector := o.selector
	if selector == nil {
		selector = review.NewSelector()
	}
	return selector.Select(assessor.Assess(diffs))
}

// acceptAsVerified converts raw agent findings into verified findings with
// full confidence, without any LLM verification (FR-35,
// CLAUDE.v.1.2.md: verifier-skip semantics).
func acceptAsVerified(findings []agents.AgentFinding) []agents.VerifiedFinding {
	out := make([]agents.VerifiedFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, agents.VerifiedFinding{AgentFinding: f, Confidence: 1.0})
	}
	return out
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

// SetEventChan wires the SSE review-stream sink (v1.7 FR-44). Every
// subsequent Run emits its lifecycle events (status, agent_start, tool_call,
// finding, done) into ch non-blockingly; pass nil to disable. Must be called
// before Run.
func (o *Orchestrator) SetEventChan(ch chan<- ReviewEvent) {
	o.events = ch
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
	res := &Result{
		Input:        in,
		AgentErrors:  make(map[string]string),
		AgentResults: make(map[string]harness.AgentResult),
	}

	// Step 2: parse the diff.
	files, err := diff.Parse(in.DiffText)
	if err != nil {
		return nil, fmt.Errorf("parse diff: %w", err)
	}

	// emit is the run-scoped event emitter (v1.7 FR-44): it stamps every
	// event with the review identity so the SSE broker can route it.
	emit := func(ev ReviewEvent) {
		if ev.Data == nil {
			ev.Data = map[string]any{}
		}
		ev.Data["repo_full_name"] = in.RepoFullName
		ev.Data["pr_number"] = in.PRNumber
		o.emitEvent(ev)
	}
	emitStatus := func(stage, message string, extra map[string]any) {
		data := map[string]any{"stage": stage, "message": message}
		for k, v := range extra {
			data[k] = v
		}
		emit(ReviewEvent{Event: EventStatus, Data: data})
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

	// Step 3.4: adaptive review strategy (v1.2 FR-34..FR-38). A fully
	// deterministic complexity assessment on the filtered diff selects
	// which agents run, before any agent or LLM is invoked. An empty or
	// fully filtered diff assesses as Trivial (spec 14.4).
	strategy := o.selectStrategy(reviewable)
	res.ComplexityLevel = strategy.Level
	res.AgentStatus = make(map[string]string, len(o.agents))
	for _, a := range o.agents {
		if review.IsAgentEnabled(strategy, a.Name()) {
			res.AgentStatus[a.Name()] = "executed"
		} else {
			res.AgentStatus[a.Name()] = "skipped_by_strategy"
		}
	}
	o.logger.Sugar().Infow("review strategy selected",
		"level", strategy.Level,
		"agents", strategy.EnabledAgents,
		"verifier", strategy.EnableVerifier)
	emit(ReviewEvent{Event: EventStatus, Data: map[string]any{
		"stage": "strategy", "message": "复杂度评估: " + strategy.Level,
	}})

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
		emit(ReviewEvent{Event: EventDone, Data: map[string]any{
			"findings": 0, "duration_ms": res.Duration.Milliseconds(),
			"markdown": "LGTM ✅（无有效代码变更，已跳过审查）",
		}})
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
		emitStatus("static-analysis", "正在运行静态分析...", nil)
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
			emitStatus("static-analysis", fmt.Sprintf("静态分析完成，发现 %d 条已知问题", len(preFindings)),
				map[string]any{"findings": len(preFindings)})
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
		if !review.IsAgentEnabled(strategy, agent.Name()) {
			o.logger.Sugar().Infow("review agent skipped by strategy",
				"agent", agent.Name(), "level", strategy.Level)
			continue
		}
		g.Go(func() error {
			agentStart := time.Now()
			emit(ReviewEvent{Event: EventAgentStart, Data: map[string]any{"agent": agent.Name()}})
			// Per-agent tool copies: every tool call is reported to the
			// event stream under this agent's name (v1.7 FR-44).
			agentRunInput := agentInput
			agentRunInput.Tools = wrapTools(agentInput.Tools, agent.Name(), emit)
			result, err := o.runAgent(ctx, agent, agentRunInput)
			mu.Lock()
			defer mu.Unlock()
			res.AgentResults[agent.Name()] = result
			if err != nil {
				reason := result.ErrorReason
				if reason == "" {
					reason = err.Error()
				}
				res.AgentErrors[agent.Name()] = reason
				o.logger.Sugar().Errorw("review agent failed",
					"agent", agent.Name(), "error", err,
					"status", result.Status,
					"elapsed", time.Since(agentStart))
			}
			// Failed results never reach downstream verification (T52). A
			// partial result interrupted after producing findings is still
			// usable and is preserved (spec 17.4).
			if result.Status == harness.StatusFailed {
				res.AgentStatus[agent.Name()] = "failed"
				return nil // do not fail the group
			}
			res.AgentStatus[agent.Name()] = "executed"
			if result.Status == harness.StatusPartial {
				o.logger.Sugar().Warnw("review agent result is partial",
					"agent", agent.Name(), "reason", result.ErrorReason,
					"tool_calls", result.ToolCallCount)
			}
			fnd := result.Findings
			rawFindings = append(rawFindings, fnd...)
			for _, f := range fnd {
				data, err := json.Marshal(f)
				if err == nil {
					var payload map[string]any
					if err := json.Unmarshal(data, &payload); err == nil {
						emit(ReviewEvent{Event: EventFinding, Data: payload})
					}
				}
			}
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

	// Step 6: adversarial verification (FR-10). Trivial PRs skip the
	// verifier entirely: the raw findings are accepted with full confidence
	// and no LLM call (FR-35, CLAUDE.v.1.2.md). Otherwise the verifier is
	// built per run because its read_file tool is bound to this run's
	// workspace.
	switch {
	case !strategy.EnableVerifier:
		res.Findings = acceptAsVerified(rawFindings)
		o.logger.Sugar().Infow("verifier skipped by strategy", "level", strategy.Level)
	default:
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
	warnings := res.AgentWarnings()
	emit(ReviewEvent{Event: EventDone, Data: map[string]any{
		"findings":    len(res.Findings),
		"duration_ms": res.Duration.Milliseconds(),
		"markdown":    finalMarkdown(res.Findings, warnings),
		"warnings":    warnings,
	}})
	return res, nil
}

// finalMarkdown renders the verified findings as a compact markdown report
// for the SSE review stream (v1.7 FR-44 done event). Agent warnings from the
// v1.8 Harness (failed / partial) are rendered first (FR-49).
func finalMarkdown(findings []agents.VerifiedFinding, warnings []string) string {
	var b strings.Builder
	if len(warnings) > 0 {
		for _, warning := range warnings {
			fmt.Fprintf(&b, "> %s\n", warning)
		}
		b.WriteString("\n")
	}
	if len(findings) == 0 {
		b.WriteString("LGTM ✅（未发现问题）")
		return b.String()
	}
	fmt.Fprintf(&b, "## 审查结果（%d 条发现）\n\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "### %s `%s:%d`\n\n", strings.ToUpper(f.Severity), f.FilePath, f.LineNumber)
		fmt.Fprintf(&b, "%s\n\n", f.Message)
		if f.CodeSnippet != "" {
			b.WriteString("```go\n")
			b.WriteString(f.CodeSnippet)
			b.WriteString("\n```\n\n")
		}
		if f.Suggestion != "" {
			fmt.Fprintf(&b, "**建议**: %s\n\n", f.Suggestion)
		}
	}
	return b.String()
}

// runAgent executes one agent through the Harness control layer (AC-22,
// FR-48). The Harness applies the per-agent timeout, retry policy and
// oscillation detection and decides the final status.
func (o *Orchestrator) runAgent(ctx context.Context, a agents.ReviewAgent, input agents.ReviewInput) (harness.AgentResult, error) {
	runner := o.runner
	if runner == nil {
		runner = harness.New(o.logger, harness.WithTimeout(o.agentTimeout))
	}
	return runner.Execute(ctx, a, input)
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
