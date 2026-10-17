package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codereviewagent/internal/ai/agents"
)

// MockOrchestrator is a scripted ReviewRunner for tests (CLAUDE.v.1.6.md:
// evaluator tests must never issue real LLM requests). It returns preset
// verified findings.
type MockOrchestrator struct {
	findings []agents.VerifiedFinding
	err      error
	calls    int
}

// RunReview implements ReviewRunner.
func (m *MockOrchestrator) RunReview(_ context.Context, diffText, root string) ([]agents.VerifiedFinding, error) {
	m.calls++
	_, _ = diffText, root
	return m.findings, m.err
}

// verified builds a verified finding for test fixtures.
func verified(agent, severity, file string, line int, msg string) agents.VerifiedFinding {
	return agents.VerifiedFinding{
		AgentFinding: agents.AgentFinding{
			Agent: agent, Severity: severity, FilePath: file,
			LineNumber: line, Message: msg, Confidence: 0.9,
		},
	}
}

// sqlInjectionGT is the classic ground truth item used across tests.
func sqlInjectionGT() GroundTruthItem {
	return GroundTruthItem{
		FilePath: "db.go", Line: 10, Severity: "critical",
		Category: "security", Message: "SQL injection via concatenation",
	}
}

func TestMatchFindingsLineTolerance(t *testing.T) {
	gt := []GroundTruthItem{sqlInjectionGT()}

	// Within +/- 2 lines: matched.
	for _, line := range []int{8, 9, 10, 11, 12} {
		tp, fn, ubv := matchFindings([]agents.VerifiedFinding{
			verified("security", "critical", "db.go", line, "SQL injection"),
		}, gt)
		if tp != 1 || fn != 0 || len(ubv) != 0 {
			t.Fatalf("line %d: tp=%d fn=%d ubv=%d, want 1/0/0", line, tp, fn, len(ubv))
		}
	}

	// Beyond +/- 2 lines: UBV + FN.
	tp, fn, ubv := matchFindings([]agents.VerifiedFinding{
		verified("security", "critical", "db.go", 13, "SQL injection"),
	}, gt)
	if tp != 0 || fn != 1 || len(ubv) != 1 {
		t.Fatalf("line 13: tp=%d fn=%d ubv=%d, want 0/1/1", tp, fn, len(ubv))
	}

	// Wrong category: UBV + FN.
	tp, fn, ubv = matchFindings([]agents.VerifiedFinding{
		verified("logic", "critical", "db.go", 10, "something else"),
	}, gt)
	if tp != 0 || fn != 1 || len(ubv) != 1 {
		t.Fatalf("wrong category: tp=%d fn=%d ubv=%d, want 0/1/1", tp, fn, len(ubv))
	}

	// The lint agent maps to the "style" category.
	styleGT := []GroundTruthItem{{FilePath: "a.go", Line: 3, Category: "style"}}
	tp, fn, ubv = matchFindings([]agents.VerifiedFinding{
		verified("lint", "suggestion", "a.go", 3, "naming"),
	}, styleGT)
	if tp != 1 || fn != 0 || len(ubv) != 0 {
		t.Fatalf("lint/style mapping: tp=%d fn=%d ubv=%d, want 1/0/0", tp, fn, len(ubv))
	}
}

func TestEvaluatorMatchesGroundTruth(t *testing.T) {
	// AC-62: GT has 1 SQL injection, the agent finds it: TP=1, FN=0.
	mock := &MockOrchestrator{findings: []agents.VerifiedFinding{
		verified("security", "critical", "db.go", 10, "SQL injection via concatenation"),
	}}
	e := &Evaluator{Runner: mock}
	samples := []Sample{{Name: "s1", DiffText: "diff", GroundTruth: []GroundTruthItem{sqlInjectionGT()}}}

	metrics, ubv, failed := e.Evaluate(context.Background(), samples)
	require.Empty(t, failed)
	assert.Empty(t, ubv)
	assert.Equal(t, 1, metrics.TruePositives)
	assert.Equal(t, 0, metrics.FalseNegatives)
	assert.Equal(t, 0, metrics.UnreviewedDiscovery)
	assert.InDelta(t, 1.0, metrics.Precision, 1e-9)
	assert.InDelta(t, 1.0, metrics.Recall, 1e-9)
}

func TestEvaluatorUBVInsteadOfFalsePositive(t *testing.T) {
	// AC-63: the agent also reports a logic error not in the GT. It must
	// NOT be counted as FP: it lands in UBVItems instead.
	mock := &MockOrchestrator{findings: []agents.VerifiedFinding{
		verified("security", "critical", "db.go", 10, "SQL injection via concatenation"),
		verified("logic", "warning", "db.go", 16, "unhandled error"),
	}}
	e := &Evaluator{Runner: mock}
	samples := []Sample{{Name: "s1", DiffText: "diff", GroundTruth: []GroundTruthItem{sqlInjectionGT()}}}

	metrics, ubv, failed := e.Evaluate(context.Background(), samples)
	require.Empty(t, failed)
	require.Len(t, ubv, 1)
	assert.Equal(t, "logic", ubv[0].Agent)
	assert.Equal(t, 1, metrics.TruePositives)
	assert.Equal(t, 0, metrics.FalseNegatives)
	assert.Equal(t, 1, metrics.UnreviewedDiscovery)
	// Worst-case precision: TP/(TP+UBV) = 0.5, recall = 1.
	assert.InDelta(t, 0.5, metrics.Precision, 1e-9)
	assert.InDelta(t, 1.0, metrics.Recall, 1e-9)
}

func TestEvaluatorDelta(t *testing.T) {
	// AC-64: baseline precision 50%, current 60% -> delta exactly 0.1.
	baseline := &EvalMetrics{Precision: 0.5, Recall: 0.8}
	current := EvalMetrics{Precision: 0.6, Recall: 0.75}

	report := BuildReport("current", "v1.1", current, baseline, nil)
	assert.InDelta(t, 0.1, report.PrecisionDelta, 1e-12)
	assert.InDelta(t, -0.05, report.RecallDelta, 1e-12)
	assert.Equal(t, "current", report.Version)
	assert.Equal(t, "v1.1", report.BaselineVersion)

	// No baseline: zero deltas.
	report = BuildReport("current", "", current, nil, nil)
	assert.Zero(t, report.PrecisionDelta)
	assert.Zero(t, report.RecallDelta)
}

func TestEvaluatorFailedSampleSkipped(t *testing.T) {
	// Spec 13.4: a failed review is recorded and skipped, the rest still
	// computes.
	mock := &MockOrchestrator{err: errors.New("LLM API timeout")}
	e := &Evaluator{Runner: mock}
	samples := []Sample{
		{Name: "s1", GroundTruth: []GroundTruthItem{sqlInjectionGT()}},
		{Name: "s2", GroundTruth: []GroundTruthItem{sqlInjectionGT()}},
	}

	metrics, ubv, failed := e.Evaluate(context.Background(), samples)
	require.Len(t, failed, 2)
	assert.Empty(t, ubv)
	assert.Zero(t, metrics.TruePositives)
	assert.Equal(t, 2, mock.calls)
}

func TestComputeMetricsZeroDenominators(t *testing.T) {
	m := ComputeMetrics(0, 0, 0)
	assert.Zero(t, m.Precision)
	assert.Zero(t, m.Recall)
}

func TestLoadSamples(t *testing.T) {
	// A well-formed dataset round-trips.
	dir := t.TempDir()
	sampleDir := filepath.Join(dir, "sample-1")
	require.NoError(t, os.MkdirAll(filepath.Join(sampleDir, "source_files"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sampleDir, "pr.diff"), []byte("diff --git"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sampleDir, "ground_truth.json"),
		[]byte(`[{"FilePath":"db.go","Line":10,"Severity":"critical","Category":"security","Message":"SQL injection"}]`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sampleDir, "source_files", "db.go"), []byte("package sample\n"), 0o644))

	samples, err := LoadSamples(dir)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, "sample-1", samples[0].Name)
	assert.Equal(t, "diff --git", samples[0].DiffText)
	require.Len(t, samples[0].GroundTruth, 1)
	assert.Equal(t, "db.go", samples[0].GroundTruth[0].FilePath)
	assert.Equal(t, filepath.Join(sampleDir, "source_files"), samples[0].Root)

	// An empty dataset is an error (spec 13.4).
	empty := t.TempDir()
	_, err = LoadSamples(empty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 1 sample")
}
