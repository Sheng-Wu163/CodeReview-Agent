package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
	ltools "github.com/tmc/langchaingo/tools"
	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
	itools "codereviewagent/internal/ai/tools"
	ctxcontext "codereviewagent/internal/context"
	"codereviewagent/internal/diff"
	"codereviewagent/internal/harness"
	"codereviewagent/internal/repository"
	"codereviewagent/internal/review"
)

// nopLogger returns a discard logger for tests.
func nopLogger(t *testing.T) *zap.Logger {
	t.Helper()
	return zap.NewNop()
}

// mockAgent is a scripted ReviewAgent for orchestrator tests. When useTools
// is set it performs one tool call so the v1.8 completion gate marks it
// completed instead of partial.
type mockAgent struct {
	name      string
	findings  []agents.AgentFinding
	err       error
	delay     time.Duration
	useTools  bool
	toolInput string

	mu    sync.Mutex
	calls int
}

func (m *mockAgent) Name() string { return m.name }

func (m *mockAgent) Review(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.useTools && len(input.Tools) > 0 {
		if _, err := input.Tools[0].Call(ctx, m.toolInput); err != nil {
			return nil, err
		}
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
	before   func()
}

func (m *mockVerifier) Verify(_ context.Context, findings []agents.AgentFinding, _ agents.ReviewInput) ([]agents.VerifiedFinding, error) {
	m.calls++
	if m.before != nil {
		m.before()
	}
	return m.verified, nil
}

// passthroughVerifier accepts every raw finding with full confidence.
type passthroughVerifier struct{}

func (passthroughVerifier) Verify(_ context.Context, findings []agents.AgentFinding, _ agents.ReviewInput) ([]agents.VerifiedFinding, error) {
	out := make([]agents.VerifiedFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, agents.VerifiedFinding{AgentFinding: f, Confidence: 1})
	}
	return out, nil
}

// mockCheckpointStore is an in-memory CheckpointStore.
type mockCheckpointStore struct {
	mu    sync.Mutex
	items map[string]*repository.Checkpoint
}

func newMockCheckpointStore() *mockCheckpointStore {
	return &mockCheckpointStore{items: make(map[string]*repository.Checkpoint)}
}

func checkpointKey(sha, agent string) string { return sha + "|" + agent }

func (m *mockCheckpointStore) Save(_ context.Context, cp *repository.Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *cp
	if copied.ID == 0 {
		copied.ID = len(m.items) + 1
	}
	m.items[checkpointKey(cp.HeadSHA, cp.AgentName)] = &copied
	return nil
}

func (m *mockCheckpointStore) GetBySHAAndAgent(_ context.Context, sha string, agent string) (*repository.Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp, ok := m.items[checkpointKey(sha, agent)]
	if !ok {
		return nil, fmt.Errorf("checkpoint not found")
	}
	copied := *cp
	return &copied, nil
}

func (m *mockCheckpointStore) seed(t *testing.T, sha, agent, status string, result harness.AgentResult) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	if err := m.Save(context.Background(), &repository.Checkpoint{
		RepoFullName: "a/b", PRNumber: 1, HeadSHA: sha,
		AgentName: agent, Status: status, ResultJSON: data,
	}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
}

func (m *mockCheckpointStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
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
	o := New(nopLogger(t), root, reviewAgents, factory, diff.NewFilter(diff.DefaultFilterRules()))
	// Pin the Standard strategy so the pre-v1.2 pipeline tests exercise the
	// full 4-agent + verifier flow regardless of diff size.
	o.SetReviewStrategy(fixedAssessor{level: review.ComplexityStandard}, review.NewSelector())
	return o
}

// fixedAssessor returns a constant complexity level (v1.2 tests).
type fixedAssessor struct{ level review.ComplexityLevel }

func (f fixedAssessor) Assess(_ []diff.FileDiff) review.ComplexityLevel { return f.level }

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

// fourAgents builds one mock agent per review role.
func fourAgents() ([]agents.ReviewAgent, map[string]*mockAgent) {
	m := map[string]*mockAgent{
		"security":    {name: "security", findings: []agents.AgentFinding{finding("security", "critical", "SQL injection")}},
		"logic":       {name: "logic", findings: []agents.AgentFinding{finding("logic", "warning", "unhandled error")}},
		"performance": {name: "performance"},
		"lint":        {name: "lint"},
	}
	return []agents.ReviewAgent{m["security"], m["logic"], m["performance"], m["lint"]}, m
}

// TestOrchestratorTrivialStrategySkipsAgents verifies AC-74: a Trivial PR
// runs only the security and lint agents and never invokes the verifier.
func TestOrchestratorTrivialStrategySkipsAgents(t *testing.T) {
	agts, byName := fourAgents()
	verifier := &mockVerifier{verified: []agents.VerifiedFinding{
		{AgentFinding: finding("security", "critical", "SQL injection"), Confidence: 0.95},
	}}
	o := newTestOrchestrator(t, agts, verifier)
	o.SetReviewStrategy(fixedAssessor{level: review.ComplexityTrivial}, review.NewSelector())

	res, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 1, HeadSHA: "trivial-sha", DiffText: reviewDiff(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.ComplexityLevel != review.ComplexityTrivial {
		t.Errorf("ComplexityLevel = %q, want trivial", res.ComplexityLevel)
	}
	if byName["security"].callCount() != 1 || byName["lint"].callCount() != 1 {
		t.Errorf("security/lint calls = %d/%d, want 1/1",
			byName["security"].callCount(), byName["lint"].callCount())
	}
	if byName["logic"].callCount() != 0 || byName["performance"].callCount() != 0 {
		t.Errorf("logic/performance must be skipped, got %d/%d calls",
			byName["logic"].callCount(), byName["performance"].callCount())
	}
	if verifier.calls != 0 {
		t.Errorf("verifier must be skipped for Trivial, got %d calls", verifier.calls)
	}
	// Skipped agents are recorded (AC-77) and findings are accepted with
	// full confidence without LLM verification.
	if res.AgentStatus["logic"] != "skipped_by_strategy" || res.AgentStatus["performance"] != "skipped_by_strategy" {
		t.Errorf("AgentStatus = %v, want logic/performance skipped_by_strategy", res.AgentStatus)
	}
	if res.AgentStatus["security"] != "executed" || res.AgentStatus["lint"] != "executed" {
		t.Errorf("AgentStatus = %v, want security/lint executed", res.AgentStatus)
	}
	if len(res.Findings) == 0 {
		t.Fatal("expected the raw findings to be accepted as verified")
	}
	for _, f := range res.Findings {
		if f.Confidence != 1.0 {
			t.Errorf("skipped verifier must set confidence 1.0, got %v", f.Confidence)
		}
	}
}

// TestOrchestratorStandardStrategyRunsAll verifies AC-75: a Standard PR runs
// all four agents and the verifier.
func TestOrchestratorStandardStrategyRunsAll(t *testing.T) {
	agts, byName := fourAgents()
	verifier := &mockVerifier{verified: []agents.VerifiedFinding{
		{AgentFinding: finding("security", "critical", "SQL injection"), Confidence: 0.9},
	}}
	o := newTestOrchestrator(t, agts, verifier)
	o.SetReviewStrategy(fixedAssessor{level: review.ComplexityStandard}, review.NewSelector())

	res, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 2, HeadSHA: "standard-sha", DiffText: reviewDiff(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for name, a := range byName {
		if a.callCount() != 1 {
			t.Errorf("agent %s calls = %d, want 1", name, a.callCount())
		}
		if res.AgentStatus[name] != "executed" {
			t.Errorf("AgentStatus[%s] = %q, want executed", name, res.AgentStatus[name])
		}
	}
	if verifier.calls != 1 {
		t.Errorf("verifier calls = %d, want 1", verifier.calls)
	}
}

// TestOrchestratorComplexStrategyRunsAll verifies AC-76.
func TestOrchestratorComplexStrategyRunsAll(t *testing.T) {
	agts, byName := fourAgents()
	verifier := &mockVerifier{}
	o := newTestOrchestrator(t, agts, verifier)
	o.SetReviewStrategy(fixedAssessor{level: review.ComplexityComplex}, review.NewSelector())

	res, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 3, HeadSHA: "complex-sha", DiffText: reviewDiff(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for name, a := range byName {
		if a.callCount() != 1 {
			t.Errorf("agent %s calls = %d, want 1", name, a.callCount())
		}
	}
	if verifier.calls != 1 {
		t.Errorf("verifier calls = %d, want 1", verifier.calls)
	}
	if res.ComplexityLevel != review.ComplexityComplex {
		t.Errorf("ComplexityLevel = %q, want complex", res.ComplexityLevel)
	}
}

// TestOrchestratorEmitsReviewEvents verifies the v1.7 FR-44 event stream
// (T44 completion): a mock channel receives status/agent_start/finding/done
// events in order, each with the {event, data} JSON shape.
func TestOrchestratorEmitsReviewEvents(t *testing.T) {
	agts, _ := fourAgents()
	verifier := &mockVerifier{verified: []agents.VerifiedFinding{
		{AgentFinding: finding("security", "critical", "SQL injection"), Confidence: 0.9},
	}}
	o := newTestOrchestrator(t, agts, verifier)

	ch := make(chan ReviewEvent, 128)
	o.SetEventChan(ch)

	if _, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 9, HeadSHA: "events-sha", DiffText: reviewDiff(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(ch)

	var (
		agentStarts, findingEvents, statusEvents, doneEvents int
		doneSeenAgentStart, doneSeenFinding                  bool
	)
	for ev := range ch {
		// AC-93: the wire shape must be {event: string, data: object}.
		wire, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(wire, &decoded); err != nil {
			t.Fatalf("event JSON: %v", err)
		}
		if _, ok := decoded["event"].(string); !ok {
			t.Errorf("event missing string 'event' field: %s", wire)
		}
		if _, ok := decoded["data"].(map[string]any); !ok {
			t.Errorf("event missing object 'data' field: %s", wire)
		}

		switch ev.Event {
		case EventAgentStart:
			agentStarts++
		case EventFinding:
			findingEvents++
		case EventStatus:
			statusEvents++
		case EventDone:
			doneEvents++
			doneSeenAgentStart = agentStarts > 0
			doneSeenFinding = findingEvents > 0
		}
	}

	if statusEvents == 0 {
		t.Error("expected at least one status event")
	}
	if agentStarts != 4 {
		t.Errorf("agent_start events = %d, want 4", agentStarts)
	}
	if findingEvents == 0 {
		t.Error("expected at least one finding event")
	}
	if doneEvents != 1 {
		t.Errorf("done events = %d, want 1", doneEvents)
	}
	if !doneSeenAgentStart || !doneSeenFinding {
		t.Error("done event must come after agent_start and finding events")
	}
}

// TestWrapToolsEmitsToolCall verifies the tool_call event wrapper (FR-44).
func TestWrapToolsEmitsToolCall(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := itools.ReadFileTool{Root: root}
	events := make(chan ReviewEvent, 8)
	wrapped := wrapTools([]ltools.Tool{inner}, "security", func(ev ReviewEvent) { events <- ev })

	if len(wrapped) != 1 {
		t.Fatalf("wrapped tools = %d, want 1", len(wrapped))
	}
	if _, err := wrapped[0].Call(context.Background(), "a.go"); err != nil {
		t.Fatalf("Call: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Event != EventToolCall {
			t.Errorf("event = %q, want tool_call", ev.Event)
		}
		if ev.Data["agent"] != "security" || ev.Data["tool"] != "read_file" || ev.Data["input"] != "a.go" {
			t.Errorf("tool_call data = %v", ev.Data)
		}
	default:
		t.Fatal("no tool_call event emitted")
	}
}

// TestOrchestratorSavesCheckpointsForAllAgents verifies AC-113.
func TestOrchestratorSavesCheckpointsForAllAgents(t *testing.T) {
	agts, _ := fourAgents()
	o := newTestOrchestrator(t, agts, nil)
	store := newMockCheckpointStore()
	o.SetCheckpointStore(store)

	if _, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 1, HeadSHA: "checkpoint-all", DiffText: reviewDiff(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := store.count(); got != 4 {
		t.Fatalf("checkpoints = %d, want 4", got)
	}
	for _, name := range []string{"security", "logic", "performance", "lint"} {
		cp, err := store.GetBySHAAndAgent(context.Background(), "checkpoint-all", name)
		if err != nil {
			t.Fatalf("checkpoint %s: %v", name, err)
		}
		if cp.Status != "success" {
			t.Errorf("checkpoint %s status = %q, want success", name, cp.Status)
		}
	}
}

// TestOrchestratorResumesCheckpoints verifies AC-114: successful agents are
// skipped, failed checkpoints are re-executed.
func TestOrchestratorResumesCheckpoints(t *testing.T) {
	agts, byName := fourAgents()
	o := newTestOrchestrator(t, agts, passthroughVerifier{})
	store := newMockCheckpointStore()
	store.seed(t, "resume-sha", "security", "success", harness.AgentResult{
		Status:        harness.StatusCompleted,
		Findings:      []agents.AgentFinding{finding("security", "critical", "cached security finding")},
		ToolCallCount: 1,
	})
	store.seed(t, "resume-sha", "logic", "failed", harness.AgentResult{
		Status:      harness.StatusFailed,
		ErrorReason: "llm timeout",
	})
	o.SetCheckpointStore(store)

	res, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 1, HeadSHA: "resume-sha", DiffText: reviewDiff(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if byName["security"].callCount() != 0 {
		t.Errorf("security agent calls = %d, want 0 (cached)", byName["security"].callCount())
	}
	if byName["logic"].callCount() != 1 {
		t.Errorf("logic agent calls = %d, want 1 (failed checkpoint retried)", byName["logic"].callCount())
	}
	if res.AgentResults["security"].ToolCallCount != 1 {
		t.Errorf("cached security result not restored: %+v", res.AgentResults["security"])
	}
	found := false
	for _, f := range res.Findings {
		if f.Message == "cached security finding" {
			found = true
		}
	}
	if !found {
		t.Errorf("cached security finding missing from results: %+v", res.Findings)
	}
}

// TestOrchestratorForceRerun verifies AC-115.
func TestOrchestratorForceRerun(t *testing.T) {
	agts, byName := fourAgents()
	o := newTestOrchestrator(t, agts, nil)
	store := newMockCheckpointStore()
	store.seed(t, "force-sha", "security", "success", harness.AgentResult{
		Status:        harness.StatusCompleted,
		ToolCallCount: 1,
	})
	o.SetCheckpointStore(store)
	o.SetCheckpointConfig(true, nil)

	if _, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 1, HeadSHA: "force-sha", DiffText: reviewDiff(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if byName["security"].callCount() != 1 {
		t.Errorf("security agent calls = %d, want 1 (force rerun)", byName["security"].callCount())
	}
}

// TestOrchestratorForceRerunAgents verifies the per-agent force list.
func TestOrchestratorForceRerunAgents(t *testing.T) {
	agts, byName := fourAgents()
	o := newTestOrchestrator(t, agts, nil)
	store := newMockCheckpointStore()
	store.seed(t, "force-agent-sha", "security", "success", harness.AgentResult{
		Status:        harness.StatusCompleted,
		ToolCallCount: 1,
	})
	store.seed(t, "force-agent-sha", "logic", "success", harness.AgentResult{
		Status:        harness.StatusCompleted,
		ToolCallCount: 1,
	})
	o.SetCheckpointStore(store)
	o.SetCheckpointConfig(false, []string{"security"})

	if _, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 1, HeadSHA: "force-agent-sha", DiffText: reviewDiff(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if byName["security"].callCount() != 1 {
		t.Errorf("security calls = %d, want 1 (listed for force rerun)", byName["security"].callCount())
	}
	if byName["logic"].callCount() != 0 {
		t.Errorf("logic calls = %d, want 0 (checkpoint reused)", byName["logic"].callCount())
	}
}

// TestOrchestratorCheckpointsPersistBeforeVerification verifies that agent
// results are written immediately, not at the end of the whole pipeline
// (FR-59 / AC-116).
func TestOrchestratorCheckpointsPersistBeforeVerification(t *testing.T) {
	agts, _ := fourAgents()
	store := newMockCheckpointStore()
	verifier := &mockVerifier{
		verified: []agents.VerifiedFinding{{AgentFinding: finding("security", "critical", "x"), Confidence: 0.9}},
		before: func() {
			if got := store.count(); got != 4 {
				t.Errorf("checkpoints at verification time = %d, want 4", got)
			}
		},
	}
	o := newTestOrchestrator(t, agts, verifier)
	o.SetCheckpointStore(store)

	if _, err := o.Run(context.Background(), Input{
		RepoFullName: "a/b", PRNumber: 1, HeadSHA: "persist-sha", DiffText: reviewDiff(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if verifier.calls != 1 {
		t.Errorf("verifier calls = %d, want 1", verifier.calls)
	}
}
