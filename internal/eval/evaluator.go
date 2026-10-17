package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
)

// lineTolerance is the +/- line window when matching a finding to a
// ground-truth item (CLAUDE.v.1.6.md: prevents misjudging findings that
// differ only by blank lines).
const lineTolerance = 2

// Sample is one evaluation dataset sample (FR-30): the PR diff, the
// checked-out source files the tools operate on, and the (possibly
// incomplete) human-annotated ground truth.
type Sample struct {
	// Name is the sample directory name, e.g. "sample-1".
	Name string
	// DiffText is the raw unified diff of the simulated PR (pr.diff).
	DiffText string
	// Root is the sample's source_files directory (the review workspace);
	// empty when the sample ships no sources.
	Root string
	// GroundTruth lists the human-annotated expected findings.
	GroundTruth []GroundTruthItem
}

// ReviewRunner runs the review pipeline for one sample and returns its
// verified findings. The real orchestrator is adapted to it by the eval
// CLI; tests inject a MockOrchestrator (CLAUDE.v.1.6.md mock isolation).
type ReviewRunner interface {
	// RunReview reviews diffText against the workspace rooted at root.
	RunReview(ctx context.Context, diffText, root string) ([]agents.VerifiedFinding, error)
}

// Evaluator replays dataset samples through the review pipeline and
// aggregates the matching results (FR-31/FR-32).
type Evaluator struct {
	// Runner performs the reviews.
	Runner ReviewRunner
	// Logger records per-sample failures. May be nil.
	Logger *zap.Logger
}

// Evaluate reviews every sample and aggregates TP/FN/UBV. Samples whose
// review fails (e.g. LLM API timeout) are recorded and skipped without
// aborting the run (spec 13.4). It returns the aggregate metrics, the UBV
// items and the names of the failed samples.
func (e *Evaluator) Evaluate(ctx context.Context, samples []Sample) (EvalMetrics, []agents.AgentFinding, []string) {
	log := e.logger()
	var (
		failed   []string
		ubvItems []agents.AgentFinding
		tp, fn   int
	)
	for _, s := range samples {
		findings, err := e.Runner.RunReview(ctx, s.DiffText, s.Root)
		if err != nil {
			log.Sugar().Warnw("eval: sample review failed, skipping",
				"sample", s.Name, "error", err)
			failed = append(failed, s.Name)
			continue
		}
		sTP, sFN, sUBV := matchFindings(findings, s.GroundTruth)
		tp += sTP
		fn += sFN
		ubvItems = append(ubvItems, sUBV...)
		log.Sugar().Infow("eval: sample reviewed",
			"sample", s.Name, "findings", len(findings),
			"tp", sTP, "fn", sFN, "ubv", len(sUBV))
	}
	return ComputeMetrics(tp, fn, len(ubvItems)), ubvItems, failed
}

// matchFindings matches verified findings against the ground truth
// (FilePath + line +/- lineTolerance + Category). Matched pairs count as
// true positives; ground-truth items left unmatched are false negatives;
// findings unmatched by the ground truth are NOT false positives - they
// are returned as UBV items for human review (FR-32).
func matchFindings(findings []agents.VerifiedFinding, groundTruth []GroundTruthItem) (tp, fn int, ubv []agents.AgentFinding) {
	used := make([]bool, len(groundTruth))
	tp = 0
	for _, f := range findings {
		if matchIndex(f, groundTruth, used) >= 0 {
			tp++
		} else {
			ubv = append(ubv, f.AgentFinding)
		}
	}
	for _, u := range used {
		if !u {
			fn++
		}
	}
	return tp, fn, ubv
}

// matchIndex returns the index of the first unused ground-truth item that
// matches f, or -1. The used slice is updated in place.
func matchIndex(f agents.VerifiedFinding, groundTruth []GroundTruthItem, used []bool) int {
	category := categoryOf(f.Agent)
	for i, gt := range groundTruth {
		if used[i] {
			continue
		}
		if gt.FilePath != f.FilePath {
			continue
		}
		if abs(gt.Line-f.LineNumber) > lineTolerance {
			continue
		}
		if gt.Category != category {
			continue
		}
		used[i] = true
		return i
	}
	return -1
}

// ComputeMetrics derives the aggregate metrics. Precision is the worst-case
// precision TP/(TP+UBV) - all UBV are assumed false positives until human
// review promotes them into the ground truth; recall is TP/(TP+FN).
func ComputeMetrics(tp, fn, ubv int) EvalMetrics {
	m := EvalMetrics{
		TruePositives:       tp,
		FalseNegatives:      fn,
		UnreviewedDiscovery: ubv,
	}
	if tp+ubv > 0 {
		m.Precision = round4(float64(tp) / float64(tp+ubv))
	}
	if tp+fn > 0 {
		m.Recall = round4(float64(tp) / float64(tp+fn))
	}
	return m
}

// BuildReport assembles the final report with the trend deltas relative to
// the baseline (FR-31). A nil baseline yields zero deltas.
func BuildReport(version, baselineVersion string, m EvalMetrics, baseline *EvalMetrics, ubv []agents.AgentFinding) EvalReport {
	report := EvalReport{
		Version:         version,
		BaselineVersion: baselineVersion,
		Metrics:         m,
		UBVItems:        ubv,
	}
	if baseline != nil {
		report.PrecisionDelta = round4(m.Precision - baseline.Precision)
		report.RecallDelta = round4(m.Recall - baseline.Recall)
	}
	return report
}

// LoadReport reads a previously generated metrics.json (the baseline).
func LoadReport(path string) (*EvalReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read baseline report %s: %w", path, err)
	}
	var report EvalReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parse baseline report %s: %w", path, err)
	}
	return &report, nil
}

// LoadSamples reads all dataset samples under dir (FR-30): each sample
// subdirectory must contain pr.diff and ground_truth.json and may contain
// source_files/. An empty dataset is an error (spec 13.4).
func LoadSamples(dir string) ([]Sample, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dataset dir %s: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("eval: dataset %s is empty, provide at least 1 sample", dir)
	}

	samples := make([]Sample, 0, len(names))
	for _, name := range names {
		sampleDir := filepath.Join(dir, name)
		diffText, err := os.ReadFile(filepath.Join(sampleDir, "pr.diff"))
		if err != nil {
			return nil, fmt.Errorf("read %s/pr.diff: %w", sampleDir, err)
		}
		gt, err := loadGroundTruth(filepath.Join(sampleDir, "ground_truth.json"))
		if err != nil {
			return nil, fmt.Errorf("load %s/ground_truth.json: %w", sampleDir, err)
		}
		s := Sample{
			Name:        name,
			DiffText:    string(diffText),
			Root:        filepath.Join(sampleDir, "source_files"),
			GroundTruth: gt,
		}
		samples = append(samples, s)
	}
	return samples, nil
}

// loadGroundTruth parses a ground_truth.json file (a JSON array of
// GroundTruthItem).
func loadGroundTruth(path string) ([]GroundTruthItem, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var items []GroundTruthItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// abs returns the absolute value of an int.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// round4 rounds to 4 decimal places so that deltas like 0.6-0.5 serialize
// as exactly 0.1 (AC-64).
func round4(x float64) float64 {
	return math.Round(x*10000) / 10000
}

// logger returns the configured logger or a no-op logger.
func (e *Evaluator) logger() *zap.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return zap.NewNop()
}
