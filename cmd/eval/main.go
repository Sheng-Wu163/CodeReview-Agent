// Command eval is the offline evaluation executor (v1.6 FR-30..FR-33): it
// replays the eval/dataset samples through the full review pipeline,
// matches the findings against the human ground truth and writes
// eval/metrics.json and eval/report.md with the relative trend versus a
// baseline run.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"codereviewagent/internal/ai"
	"codereviewagent/internal/ai/agents"
	"codereviewagent/internal/ai/tools"
	"codereviewagent/internal/config"
	"codereviewagent/internal/eval"
	"codereviewagent/internal/orchestrator"
	"codereviewagent/internal/review"
)

// perSampleTimeout bounds one full review run (the orchestrator applies its
// own per-agent timeouts on top of this).
const perSampleTimeout = 15 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configFlag   = flag.String("config", "configs/config.yaml", "config file path")
		datasetFlag  = flag.String("dataset", "eval/dataset", "dataset directory (FR-30)")
		outFlag      = flag.String("out", "eval", "output directory for report.md and metrics.json")
		baselineFlag = flag.String("baseline", "", "baseline metrics.json path (default: <out>/metrics.json when present)")
		versionFlag  = flag.String("version", "current", "version label of this evaluation run")
	)
	flag.Parse()

	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	// Dataset: an empty dataset aborts the run (spec 13.4).
	samples, err := eval.LoadSamples(*datasetFlag)
	if err != nil {
		return err
	}

	// Baseline: the explicit flag wins; otherwise reuse the previous run's
	// metrics.json so that a second `make eval` automatically reports the
	// relative trend (FR-31).
	baselinePath := *baselineFlag
	if baselinePath == "" {
		candidate := filepath.Join(*outFlag, "metrics.json")
		if _, statErr := os.Stat(candidate); statErr == nil {
			baselinePath = candidate
		}
	}
	var baseline *eval.EvalReport
	baselineVersion := ""
	if baselinePath != "" {
		baseline, err = eval.LoadReport(baselinePath)
		if err != nil {
			return err
		}
		baselineVersion = baseline.Version
		logger.Sugar().Infow("eval: using baseline", "path", baselinePath, "version", baselineVersion)
	}

	// Full review pipeline: LLM + 4 review agents + verifier + orchestrator
	// (same wiring as the server, FR-33).
	cfg, err := config.Load(*configFlag)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Evaluation runs start with cold caches: the first golangci-lint
	// compile of a fresh module can exceed the default 30s per-file
	// timeout, so give the analyzers more headroom.
	if cfg.StaticAnalysis.PerFileTimeoutSeconds < 120 {
		cfg.StaticAnalysis.PerFileTimeoutSeconds = 120
	}
	llm, err := ai.NewLLM(cfg.LLMProvider, apiKeyFor(cfg))
	if err != nil {
		return fmt.Errorf("init LLM: %w", err)
	}
	reviewAgents := []agents.ReviewAgent{
		agents.NewSecurityAgent(llm),
		agents.NewLogicAgent(llm),
		agents.NewPerformanceAgent(llm),
		agents.NewLintAgent(llm),
	}
	orch := orchestrator.New(logger, "", reviewAgents,
		func(root string) orchestrator.Verifier {
			return agents.NewVerifier(tools.ReadFileTool{Root: root}, nil)
		}, nil)
	orch.SetAnalysisConfig(cfg.StaticAnalysis)
	orch.SetReviewStrategy(review.NewAssessor(review.AssessorOptions{
		TrivialMaxLines: cfg.StaticAnalysis.Adaptive.TrivialMaxLines,
		ComplexMinLines: cfg.StaticAnalysis.Adaptive.ComplexMinLines,
		ComplexMinFiles: cfg.StaticAnalysis.Adaptive.ComplexMinFiles,
		SensitivePaths:  cfg.StaticAnalysis.Adaptive.SensitivePaths,
	}), review.NewSelector())

	evaluator := &eval.Evaluator{
		Runner: orchestratorRunner{orch: orch},
		Logger: logger,
	}
	metrics, ubv, failed := evaluator.Evaluate(context.Background(), samples)

	var baselineMetrics *eval.EvalMetrics
	if baseline != nil {
		baselineMetrics = &baseline.Metrics
	}
	report := eval.BuildReport(*versionFlag, baselineVersion, metrics, baselineMetrics, ubv)

	if err := writeOutputs(*outFlag, report); err != nil {
		return err
	}
	printSummary(samples, failed, report, baseline != nil)
	return nil
}

// orchestratorRunner adapts the orchestrator to the eval.ReviewRunner
// interface (FR-33). Each sample gets a unique head SHA so the
// orchestrator's 30s duplicate window (AC-25) never rejects consecutive
// samples.
type orchestratorRunner struct {
	orch *orchestrator.Orchestrator
}

// RunReview implements eval.ReviewRunner.
func (r orchestratorRunner) RunReview(ctx context.Context, diffText, root string) ([]agents.VerifiedFinding, error) {
	ctx, cancel := context.WithTimeout(ctx, perSampleTimeout)
	defer cancel()

	// Static analysis tools require an absolute workspace root (semgrep
	// rejects relative scanning roots).
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}

	res, err := r.orch.Run(ctx, orchestrator.Input{
		RepoFullName:  "eval/local",
		PRNumber:      1,
		HeadSHA:       sampleSHA(diffText),
		DiffText:      diffText,
		WorkspaceRoot: root,
	})
	if err != nil {
		return nil, err
	}
	return res.Findings, nil
}

// runID distinguishes consecutive evaluation runs of the same dataset so
// the orchestrator duplicate window never rejects a re-run within 30s.
var runID = time.Now().UnixNano()

// sampleSHA derives a deterministic, run-unique head SHA from the diff.
func sampleSHA(diffText string) string {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s|%d", diffText, runID)
	return hex.EncodeToString(h.Sum(nil))
}

// apiKeyFor returns the API key matching the configured LLM provider.
func apiKeyFor(cfg *config.Config) string {
	if cfg.LLMProvider == ai.ProviderAnthropic {
		return cfg.AnthropicAPIKey
	}
	return cfg.OpenAIAPIKey
}

// writeOutputs writes eval/metrics.json and eval/report.md (FR-33).
func writeOutputs(outDir string, report eval.EvalReport) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}

	metricsData, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metrics.json: %w", err)
	}
	metricsPath := filepath.Join(outDir, "metrics.json")
	if err := os.WriteFile(metricsPath, append(metricsData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write metrics.json: %w", err)
	}

	reportPath := filepath.Join(outDir, "report.md")
	if err := os.WriteFile(reportPath, []byte(eval.RenderMarkdown(report)), 0o644); err != nil {
		return fmt.Errorf("write report.md: %w", err)
	}
	return nil
}

// printSummary prints the console evaluation summary (AC-66).
func printSummary(samples []eval.Sample, failed []string, report eval.EvalReport, hasBaseline bool) {
	m := report.Metrics
	fmt.Printf("===== 评估摘要 =====\n")
	fmt.Printf("样本: %d（失败跳过: %d）\n", len(samples), len(failed))
	fmt.Printf("TP=%d  FN=%d  UBV=%d\n", m.TruePositives, m.FalseNegatives, m.UnreviewedDiscovery)
	fmt.Printf("Precision=%.1f%%  Recall=%.1f%%\n", m.Precision*100, m.Recall*100)
	if hasBaseline {
		fmt.Printf("相对基线 %s: precision_delta=%+.3f  recall_delta=%+.3f\n",
			report.BaselineVersion, report.PrecisionDelta, report.RecallDelta)
	} else {
		fmt.Printf("（未提供基线，本次结果将作为下次的基线）\n")
	}
	if len(failed) > 0 {
		fmt.Printf("失败样本: %v\n", failed)
	}
	if m.UnreviewedDiscovery > 0 {
		fmt.Printf("待复核发现 (UBV): %d 条，详见 eval/report.md\n", m.UnreviewedDiscovery)
	}
	fmt.Printf("输出: eval/metrics.json, eval/report.md\n")
}
