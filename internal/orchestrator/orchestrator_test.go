package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
	itools "codereviewagent/internal/ai/tools"
	ctxcontext "codereviewagent/internal/context"
	"codereviewagent/internal/diff"
)

// nopLogger returns a discard logger for tests.
func nopLogger(t *testing.T) *zap.Logger {
	t.Helper()
	return zap.NewNop()
}

// mockAgent is a scripted ReviewAgent for orchestrator tests.
type mockAgent struct {
	name     string
	findings []agents.AgentFinding
	err      error
	delay    time.Duration

	mu    sync.Mutex
	calls int
}

func (m *mockAgent) Name() string { return m.name }

func (m *mockAgent) Review(_ context.Context, _ agents.ReviewInput) ([]agents.AgentFinding, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return m.findings, m.err
}

func (m *mockAgent) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// mockVerifier is a scripted Verifier.
type mockVerifier struct {
	verified []agents.VerifiedFinding
	calls    int
}

func (m *mockVerifier) Verify(_ context.Context, findings []agents.AgentFinding, _ agents.ReviewInput) ([]agents.VerifiedFinding, error) {
	m.calls++
	return m.verified, nil
}

// reviewDiff is a realistic diff with a reviewable file and a vendor file.
func reviewDiff() string {
	return `diff --git a/main.go b/main.go
index 83db48f..2c47bf9 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+db.Query("SELECT * FROM users WHERE id = " + userID)

 func main() {}
diff --git a/vendor/github.com/x/y.go b/vendor/github.com/x/y.go
index 83db48f..0000000 100644
--- a/vendor/github.com/x/y.go
+++ /dev/null
@@ -1 +0,0 @@
-package y
`
}

// finding is a small helper to build findings in tests.
func finding(agent, severity, msg string) agents.AgentFinding {
	return agents.AgentFinding{
		Agent: agent, Severity: severity,
		FilePath: "main.go", LineNumber: 2,
		Message: msg, Confidence: 0.8,
	}
}

func newTestOrchestrator(t *testing.T, reviewAgents []agents.ReviewAgent, v Verifier) *Orchestrator {
	t.Helper()
	root := t.TempDir()
	var factory VerifierFactory
	if v != nil {
		factory = func(string) Verifier { return v }
	}
	return New(nopLogger(t), root, reviewAgents, factory, diff.NewFilter(diff.DefaultFilterRules()))
}

func TestOrchestratorFullPipeline(t *testing.T) {
	sec := &mockAgent{name: "security", findings: []agents.AgentFinding{finding("security", "critical", "SQL injection")}}
	logic := &mockAgent{name: "logic", findings: []agents.AgentFinding{finding("logic", "warning", "unhandled error")}}
	verifier := &mockVerifier{verified: []agents.VerifiedFinding{
		{AgentFinding: finding("security", "critical", "SQL injection"), Confidence: 0.95},
		{AgentFinding: finding("logic", "warning", "unhandled error"), Confidence: 0.9},
	}}

	o := newTestOrchestrator(t, []agents.ReviewAgent{sec, logic}, verifier)
	res, err := o.Run(context.Background(), Input{RepoFullName: "a/b", PRNumber: 1, HeadSHA: "sha1", DiffText: reviewDiff()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// AC-05: vendor file filtered, main.go reviewed.
	if len(res.ReviewedFiles) != 1 || res.ReviewedFiles[0] != "main.go" {
		t.Errorf("ReviewedFiles = %v, want [main.go]", res.ReviewedFiles)
	}
	if len(res.SkippedFiles) != 1 || !strings.Contains(res.SkippedFiles[0], "vendor/github.com/x/y.go") {
		t.Errorf("SkippedFiles = %v, want vendor file", res.SkippedFiles)
	}
	// Both agents ran.
	if sec.callCount() != 1 || logic.callCount() != 1 {
		t.Errorf("agent calls = %d/%d, want 1/1", sec.callCount(), logic.callCount())
	}
	// Verifier saw all raw findings.
	if verifier.calls != 1 {
		t.Errorf("verifier calls = %d, want 1", verifier.calls)
	}
	// Findings sorted: critical before warning (FR-13).
	if len(res.Findings) != 2 ||
		res.Findings[0].Severity != agents.SeverityCritical ||
		res.Findings[1].Severity != agents.SeverityWarning {
		t.Errorf("Findings = %+v, want [critical, warning]", res.Findings)
	}
}

func TestOrchestratorAgentFailureDoesNotBlockOthers(t *testing.T) {
	failing := &mockAgent{name: "performance", err: fmt.Errorf("llm timeout")}
	healthy := &mockAgent{name: "security", findings: []agents.AgentFinding{finding("security", "critical", "SQL injection")}}
	verifier := &mockVerifier{verified: []agents.VerifiedFinding{
		{AgentFinding: finding("security", "critical", "SQL injection"), Confidence: 0.9},
	}}

	o := newTestOrchestrator(t, []agents.ReviewAgent{failing, healthy}, verifier)
	res, err := o.Run(context.Background(), Input{RepoFullName: "a/b", PRNumber: 1, HeadSHA: "sha2", DiffText: reviewDiff()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// AC-14: other agents complete, failed agent recorded in AgentErrors.
	if _, ok := res.AgentErrors["performance"]; !ok {
		t.Errorf("AgentErrors = %v, want performance failure recorded", res.AgentErrors)
	}
	if len(res.Findings) != 1 {
		t.Errorf("Findings = %d, want the healthy agent's 1 finding", len(res.Findings))
	}
}

func TestOrchestratorRunsAgentsInParallel(t *testing.T) {
	delay := 150 * time.Millisecond
	a1 := &mockAgent{name: "a1", delay: delay}
	a2 := &mockAgent{name: "a2", delay: delay}
	a3 := &mockAgent{name: "a3", delay: delay}
	a4 := &mockAgent{name: "a4", delay: delay}

	o := newTestOrchestrator(t, []agents.ReviewAgent{a1, a2, a3, a4}, nil)
	start := time.Now()
	res, err := o.Run(context.Background(), Input{RepoFullName: "a/b", PRNumber: 1, HeadSHA: "sha3", DiffText: reviewDiff()})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// AC-13: total time close to the slowest agent, not the sum.
	if elapsed >= 3*delay {
		t.Errorf("elapsed = %v, agents did not run in parallel", elapsed)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %d, want 0", len(res.Findings))
	}
}

func TestOrchestratorNoMeaningfulChanges(t *testing.T) {
	commentOnlyDiff := `diff --git a/README.md b/README.md
index 83db48f..2c47bf9 100644
--- a/README.md
+++ b/README.md
@@ -1,3 +1,4 @@
 # Title
+just a doc line
 more text
`
	agent := &mockAgent{name: "security"}
	o := newTestOrchestrator(t, []agents.ReviewAgent{agent}, nil)

	res, err := o.Run(context.Background(), Input{RepoFullName: "a/b", PRNumber: 1, HeadSHA: "sha4", DiffText: commentOnlyDiff})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.NoMeaningfulChanges {
		t.Error("NoMeaningfulChanges = false, want true (README.md is filtered anyway, and no code file)")
	}
	if agent.callCount() != 0 {
		t.Errorf("agents should not run for comment-only diffs, got %d calls", agent.callCount())
	}
}

func TestOrchestratorBinaryFileSkipped(t *testing.T) {
	binaryDiff := `diff --git a/assets/logo.png b/assets/logo.png
new file mode 100644
index 0000000..e69de29
Binary files /dev/null and b/assets/logo.png differ
`
	agent := &mockAgent{name: "security"}
	o := newTestOrchestrator(t, []agents.ReviewAgent{agent}, nil)

	res, err := o.Run(context.Background(), Input{RepoFullName: "a/b", PRNumber: 1, HeadSHA: "sha5", DiffText: binaryDiff})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.NoMeaningfulChanges {
		t.Error("NoMeaningfulChanges = false, want true (only a binary file changed)")
	}
	if len(res.SkippedFiles) != 1 || !strings.Contains(res.SkippedFiles[0], "binary") {
		t.Errorf("SkippedFiles = %v, want binary skip", res.SkippedFiles)
	}
}

func TestOrchestratorDuplicatePRIgnored(t *testing.T) {
	o := newTestOrchestrator(t, []agents.ReviewAgent{&mockAgent{name: "security"}}, nil)
	in := Input{RepoFullName: "a/b", PRNumber: 1, HeadSHA: "same-sha", DiffText: reviewDiff()}

	if _, err := o.Run(context.Background(), in); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, err := o.Run(context.Background(), in); err == nil {
		t.Error("second Run with same head SHA should be rejected (AC-25)")
	} else if err != ErrDuplicatePR {
		t.Errorf("error = %v, want ErrDuplicatePR", err)
	}

	// A different SHA must go through.
	in.HeadSHA = "other-sha"
	if _, err := o.Run(context.Background(), in); err != nil {
		t.Fatalf("Run with other SHA: %v", err)
	}
}

func TestOrchestratorInvalidDiff(t *testing.T) {
	o := newTestOrchestrator(t, nil, nil)
	_, err := o.Run(context.Background(), Input{HeadSHA: "x", DiffText: "diff --git a/x b/x\n@@ -bad @@"})
	if err == nil {
		t.Error("malformed diff should error")
	}
}

// --- integration test: real parser, filter, agent and verifier ---

// mockLLM is a scripted llms.Model for the integration test.
type mockLLM struct {
	response string
}

func (m *mockLLM) Call(_ context.Context, prompt string, _ ...llms.CallOption) (string, error) {
	return m.response, nil
}

func (m *mockLLM) GenerateContent(_ context.Context, _ []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: m.response}},
	}, nil
}

func TestOrchestratorIntegrationRealPipeline(t *testing.T) {
	// Workspace with the vulnerable code, so the verifier can validate the
	// snippet (hallucination filter passes).
	root := t.TempDir()
	src := "package main\n\nimport \"database/sql\"\n\nfunc GetUser(db *sql.DB, userID string) (*Row, error) {\n" +
		"\trows, err := db.Query(\"SELECT * FROM users WHERE id = \" + userID)\n" +
		"\tif err != nil {\n\t\treturn nil, err\n\t}\n\tdefer rows.Close()\n\treturn nil, nil\n}\n"
	if err := os.WriteFile(filepath.Join(root, "users.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real security agent with a scripted LLM that reports the SQL injection.
	llm := &mockLLM{response: "Thought: I now know the final answer\nFinal Answer: " +
		`[{"agent":"security","severity":"critical","file_path":"users.go","line_number":5,` +
		`"message":"SQL injection via string concatenation",` +
		`"code_snippet":"db.Query(\"SELECT * FROM users WHERE id = \" + userID)",` +
		`"suggestion":"Use parameterized query","confidence":0.95}]`}
	security := agents.NewSecurityAgent(llm)

	// Real verifier built per run from the workspace root (read_file bound).
	o := New(nopLogger(t), root, []agents.ReviewAgent{security},
		func(r string) Verifier { return agents.NewVerifier(itools.ReadFileTool{Root: r}, nil) },
		diff.NewFilter(diff.DefaultFilterRules()))

	diffText := `diff --git a/users.go b/users.go
index 83db48f..2c47bf9 100644
--- a/users.go
+++ b/users.go
@@ -3,4 +3,5 @@ func GetUser(db *sql.DB, userID string) (*Row, error) {
+	rows, err := db.Query("SELECT * FROM users WHERE id = " + userID)
 	if err != nil {
 		return nil, err
 	}
`
	res, err := o.Run(context.Background(), Input{RepoFullName: "a/b", PRNumber: 7, HeadSHA: "sha-int", DiffText: diffText})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %d (%+v), want 1 verified SQL injection finding", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != agents.SeverityCritical || f.Agent != "security" {
		t.Errorf("finding = %s/%s, want security/critical", f.Agent, f.Severity)
	}
	if f.FilePath != "users.go" || f.LineNumber != 5 {
		t.Errorf("location = %s:%d, want users.go:5", f.FilePath, f.LineNumber)
	}
	if f.Confidence <= 0 || f.Confidence > 1 {
		t.Errorf("Confidence = %f, want in (0,1]", f.Confidence)
	}
	if len(res.ReviewedFiles) != 1 || res.ReviewedFiles[0] != "users.go" {
		t.Errorf("ReviewedFiles = %v, want [users.go]", res.ReviewedFiles)
	}
}

// capturingAgent records the ReviewInput it received (T26).
type capturingAgent struct {
	name string
	mu   sync.Mutex
	in   agents.ReviewInput
}

func (c *capturingAgent) Name() string { return c.name }

func (c *capturingAgent) Review(_ context.Context, in agents.ReviewInput) ([]agents.AgentFinding, error) {
	c.mu.Lock()
	c.in = in
	c.mu.Unlock()
	return nil, nil
}

// mockContextBuilder returns a fixed bundle (T26).
type mockContextBuilder struct{ bundle ctxcontext.ContextBundle }

func (m mockContextBuilder) Build(_ context.Context, _ []diff.FileDiff) (ctxcontext.ContextBundle, error) {
	return m.bundle, nil
}

// TestOrchestratorInjectsLocalContext verifies the orchestrator builds the
// local context and passes it into every agent's ReviewInput (T26).
func TestOrchestratorInjectsLocalContext(t *testing.T) {
	agent := &capturingAgent{name: "security"}
	o := newTestOrchestrator(t, []agents.ReviewAgent{agent}, nil)

	bundle := ctxcontext.ContextBundle{
		Changes: []ctxcontext.StructuralChange{{
			FilePath: "calc.go", FunctionName: "CalculateTotal", ChangeType: ctxcontext.SignatureChanged,
		}},
		Callers: []ctxcontext.CallSite{{
			FilePath: "main.go", Line: 10, Snippet: "CalculateTotal(items)", CallerFunc: "main",
		}},
		Callees: []ctxcontext.FunctionDef{{
			Package: "calc", Name: "helper", Signature: "helper(int) (int)",
		}},
	}
	o.SetContextBuilderFactory(func(string) ContextBuilder { return mockContextBuilder{bundle: bundle} })

	if _, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 1, HeadSHA: "ctxsha", DiffText: reviewDiff(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := agent.in.Context
	if len(got.Changes) != 1 || len(got.Callers) != 1 || len(got.Callees) != 1 {
		t.Fatalf("agent did not receive the context bundle: %+v", got)
	}
	if got.Callers[0].Snippet != "CalculateTotal(items)" {
		t.Errorf("caller snippet = %q, want CalculateTotal(items)", got.Callers[0].Snippet)
	}
	if got.Changes[0].ChangeType != ctxcontext.SignatureChanged {
		t.Errorf("change type = %q", got.Changes[0].ChangeType)
	}
}
