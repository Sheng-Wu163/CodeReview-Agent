package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"codereviewagent/internal/config"
	"codereviewagent/internal/diff"
)

// mockAnalyzer is a scripted Analyzer for pipeline tests.
type mockAnalyzer struct {
	name     string
	findings []PreAnalysisFinding
	err      error
	delay    time.Duration

	calls int
}

func (m *mockAnalyzer) Name() string { return m.name }

func (m *mockAnalyzer) Analyze(_ context.Context, file diff.FileDiff) ([]PreAnalysisFinding, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	// Only report findings that belong to the analyzed file.
	var out []PreAnalysisFinding
	for _, f := range m.findings {
		if f.FilePath == file.Path {
			out = append(out, f)
		}
	}
	return out, nil
}

// pipelineWorkspace writes the given files into a temp workspace.
func pipelineWorkspace(t *testing.T, files map[string]int) string {
	t.Helper()
	root := t.TempDir()
	for path, lines := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		content := ""
		for i := 0; i < lines; i++ {
			content += "line\n"
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func testFinding(tool, rule, file string, line int) PreAnalysisFinding {
	return PreAnalysisFinding{
		ID:   Fingerprint(tool, rule, file, line),
		Tool: tool, RuleID: rule, FilePath: file, Line: line,
		EndLine: line, Severity: "warning", Message: "test issue",
	}
}

func newFastPipeline(t *testing.T, analyzers ...Analyzer) *Pipeline {
	t.Helper()
	root := t.TempDir()
	// Files the mock findings reference must exist (the pipeline checks
	// readability before analyzing).
	for _, f := range []string{"a.go", "b.go", "db.go"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Pipeline{
		logger:         zap.NewNop(),
		root:           root,
		analyzers:      analyzers,
		workers:        4,
		perFileTimeout: 30 * time.Second,
	}
}

func TestPipelineRunsAnalyzersInParallel(t *testing.T) {
	// Three analyzers, each sleeping 400ms: sequential would take >= 1.2s,
	// parallel must finish close to 400ms (AC-13 style check for FR-19).
	delay := 400 * time.Millisecond
	a1 := &mockAnalyzer{name: "tool-a", delay: delay}
	a2 := &mockAnalyzer{name: "tool-b", delay: delay}
	a3 := &mockAnalyzer{name: "tool-c", delay: delay}
	p := newFastPipeline(t, a1, a2, a3)

	start := time.Now()
	res, err := p.Run(context.Background(), []diff.FileDiff{{Path: "a.go"}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed >= 1200*time.Millisecond {
		t.Errorf("elapsed = %v, analyzers did not run in parallel", elapsed)
	}
	if len(res.ByFile["a.go"]) != 0 {
		t.Errorf("findings = %d, want 0 (mocks return nothing)", len(res.ByFile["a.go"]))
	}
}

func TestPipelinePerFileTimeout(t *testing.T) {
	slow := &mockAnalyzer{name: "slow-tool", delay: 60 * time.Second}
	healthy := &mockAnalyzer{name: "fast-tool", findings: []PreAnalysisFinding{
		testFinding("fast-tool", "r1", "a.go", 3),
	}}
	p := newFastPipeline(t, slow, healthy)
	p.perFileTimeout = 300 * time.Millisecond // override for test speed

	start := time.Now()
	res, err := p.Run(context.Background(), []diff.FileDiff{{Path: "a.go"}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed >= 5*time.Second {
		t.Errorf("elapsed = %v, timeout not enforced", elapsed)
	}
	// The slow analyzer's timeout must be recorded without blocking the
	// healthy analyzer's results (FR-20).
	foundTimeout := false
	for _, f := range res.Failures {
		if f.Tool == "slow-tool" && strings.Contains(f.Err, "timeout") {
			foundTimeout = true
		}
	}
	if !foundTimeout {
		t.Errorf("Failures = %+v, want slow-tool timeout", res.Failures)
	}
	if len(res.ByFile["a.go"]) != 1 {
		t.Errorf("findings = %d, want 1 from healthy analyzer", len(res.ByFile["a.go"]))
	}
}

func TestPipelineDegradesOnError(t *testing.T) {
	failing := &mockAnalyzer{name: "tool-a", err: context.DeadlineExceeded}
	working := &mockAnalyzer{name: "tool-b", findings: []PreAnalysisFinding{
		testFinding("tool-b", "r1", "a.go", 3),
	}}
	p := newFastPipeline(t, failing, working)

	res, err := p.Run(context.Background(), []diff.FileDiff{{Path: "a.go"}})
	if err != nil {
		t.Fatalf("Run: %v (degradation must not fail the pipeline, FR-20)", err)
	}
	if len(res.Failures) != 1 || res.Failures[0].Tool != "tool-a" {
		t.Errorf("Failures = %+v, want tool-a recorded", res.Failures)
	}
	if len(res.ByFile["a.go"]) != 1 {
		t.Errorf("findings = %d, want 1 from the working analyzer", len(res.ByFile["a.go"]))
	}
}

func TestPipelineDeduplicatesByFingerprint(t *testing.T) {
	// Two analyzer instances (same tool name) reporting the identical issue:
	// the fingerprint <tool>:<ruleID>:<file>:<line> is equal, so the finding
	// is merged into one.
	a1 := &mockAnalyzer{name: "semgrep", findings: []PreAnalysisFinding{
		testFinding("semgrep", "sql-injection", "db.go", 7),
	}}
	a2 := &mockAnalyzer{name: "semgrep", findings: []PreAnalysisFinding{
		testFinding("semgrep", "sql-injection", "db.go", 7),
	}}
	p := newFastPipeline(t, a1, a2)

	res, err := p.Run(context.Background(), []diff.FileDiff{{Path: "db.go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(res.ByFile["db.go"]); got != 1 {
		t.Errorf("findings after dedup = %d, want 1", got)
	}
}

func TestPipelineGroupsByFile(t *testing.T) {
	a1 := &mockAnalyzer{name: "t1", findings: []PreAnalysisFinding{
		testFinding("t1", "r1", "a.go", 3),
		testFinding("t1", "r2", "b.go", 9),
	}}
	p := newFastPipeline(t, a1)

	res, err := p.Run(context.Background(), []diff.FileDiff{
		{Path: "a.go"}, {Path: "b.go"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.ByFile["a.go"]) != 1 || len(res.ByFile["b.go"]) != 1 {
		t.Errorf("ByFile = %+v, want one finding per file", res.ByFile)
	}
}

func TestPipelineSkipsOversizedFiles(t *testing.T) {
	root := pipelineWorkspace(t, map[string]int{"huge.go": maxFileLines + 10})
	a := &mockAnalyzer{name: "tool-a"}
	p := &Pipeline{
		logger: zap.NewNop(), root: root, analyzers: []Analyzer{a},
		workers: 4, perFileTimeout: 30 * time.Second,
	}

	res, err := p.Run(context.Background(), []diff.FileDiff{{Path: "huge.go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.calls != 0 {
		t.Errorf("analyzer called %d times for oversized file, want 0", a.calls)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != "file too large" {
		t.Errorf("Skipped = %+v, want 'file too large'", res.Skipped)
	}
}

func TestPipelineAnalyzesDuplicateFilesOnce(t *testing.T) {
	a := &mockAnalyzer{name: "tool-a", findings: []PreAnalysisFinding{
		testFinding("tool-a", "r1", "a.go", 3),
	}}
	p := newFastPipeline(t, a)

	res, err := p.Run(context.Background(), []diff.FileDiff{
		{Path: "a.go"}, {Path: "a.go"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.calls != 1 {
		t.Errorf("analyzer called %d times, want 1 (same file analyzed once)", a.calls)
	}
	if len(res.ByFile["a.go"]) != 1 {
		t.Errorf("findings = %d, want 1", len(res.ByFile["a.go"]))
	}
}

func TestPipelineSkipsDeletedFiles(t *testing.T) {
	a := &mockAnalyzer{name: "tool-a"}
	p := newFastPipeline(t, a)

	res, err := p.Run(context.Background(), []diff.FileDiff{
		{OldPath: "gone.go", ChangeType: diff.ChangeDeleted},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.calls != 0 {
		t.Errorf("analyzer called %d times for deleted file, want 0", a.calls)
	}
	if len(res.Skipped) != 1 {
		t.Errorf("Skipped = %+v, want deleted file recorded", res.Skipped)
	}
}

func TestNewPipelineRespectsConfigToggles(t *testing.T) {
	root := t.TempDir()

	// All tools disabled -> no analyzers.
	cfg := config.StaticAnalysisConfig{Enabled: true}
	cfg.Tools.Semgrep.Enabled = false
	cfg.Tools.GolangciLint.Enabled = false
	cfg.Tools.Gitleaks.Enabled = false
	p := NewPipeline(zap.NewNop(), root, cfg)
	if len(p.analyzers) != 0 {
		t.Errorf("analyzers = %d, want 0 when all toggles off (FR-20 degradation)", len(p.analyzers))
	}

	// Gitleaks custom rules mapped through.
	cfg2 := config.StaticAnalysisConfig{Enabled: true}
	cfg2.Tools.Gitleaks.Enabled = true
	cfg2.Tools.Gitleaks.CustomRules = []config.StaticGitleaksRule{
		{ID: "internal-token", Description: "internal token", Pattern: "int_tk_[A-Za-z0-9]+", Keywords: []string{"int_tk_"}},
	}
	p2 := NewPipeline(zap.NewNop(), root, cfg2)
	if len(p2.analyzers) != 1 || p2.analyzers[0].Name() != ToolGitleaks {
		t.Fatalf("analyzers = %v, want only gitleaks", p2.analyzers)
	}
	// Defaults normalized.
	if p2.workers != DefaultWorkers || p2.perFileTimeout != DefaultPerFileTimeout {
		t.Errorf("workers/timeout = %d/%v, want defaults", p2.workers, p2.perFileTimeout)
	}
}
