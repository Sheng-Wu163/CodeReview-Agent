// Package integration contains the v1.3 end-to-end acceptance test (T21):
// a simulated PR mixing all three problem classes (hardcoded secret, SQL
// injection, unhandled error) must flow through the full pipeline —
// static pre-analysis → preFindings injection → agents → verifier.
package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
	"codereviewagent/internal/analysis"
	"codereviewagent/internal/config"
	ctxcontext "codereviewagent/internal/context"
	"codereviewagent/internal/diff"
	"codereviewagent/internal/orchestrator"
	"codereviewagent/internal/review"
)

// e2eFixtureModule creates a Go module containing one file per problem
// class and returns its path plus the semgrep rules directory inside it.
func e2eFixtureModule(t *testing.T) (root, rulesDir string) {
	t.Helper()
	root = t.TempDir()
	rulesDir = filepath.Join(root, "rules")

	files := map[string]string{
		// Hardcoded secret -> gitleaks (critical).
		"secrets.go": "package main\n\nconst apiKey = \"sk-xxxxx\"\n",
		// SQL injection via concatenation -> semgrep custom rule (critical).
		"users.go": "package main\n\nimport \"database/sql\"\n\nvar db *sql.DB\n\nfunc GetUser(userID string) (*User, error) {\n\trows, err := db.Query(\"SELECT * FROM users WHERE id = \" + userID)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tdefer rows.Close()\n\treturn nil, nil\n}\n\ntype User struct{}\n",
		// Unhandled json.Unmarshal error -> golangci-lint errcheck (warning).
		"handler.go": "package main\n\nimport \"encoding/json\"\n\nfunc Handle(data []byte) {\n\tvar result map[string]string\n\tjson.Unmarshal(data, &result)\n}\n",
		// Module + lint config for golangci-lint (it needs a Go module).
		"go.mod":        "module e2efixture\n\ngo 1.26\n",
		".golangci.yml": "version: \"2\"\n\nlinters:\n  enable:\n    - errcheck\n",
	}

	for name, content := range files {
		p := filepath.Join(root, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644), "write %s", name)
	}

	// Copy the project's custom semgrep rules into the workspace.
	repoRules := filepath.Join("..", "..", "rules", "semgrep")
	entries, err := os.ReadDir(repoRules)
	require.NoError(t, err, "read repo semgrep rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repoRules, e.Name()))
		require.NoError(t, err, "read rule %s", e.Name())
		require.NoError(t, os.WriteFile(filepath.Join(rulesDir, e.Name()), b, 0o644))
	}
	return root, rulesDir
}

// e2eDiffText is the raw unified diff of the simulated PR.
func e2eDiffText() string {
	return `diff --git a/secrets.go b/secrets.go
index 83db48f..2c47bf9 100644
--- a/secrets.go
+++ b/secrets.go
@@ -1,2 +1,3 @@
 package main
+const apiKey = "sk-xxxxx"
diff --git a/users.go b/users.go
index 83db48f..2c47bf9 100644
--- a/users.go
+++ b/users.go
@@ -7,2 +8,3 @@
 func GetUser(userID string) (*User, error) {
+	rows, err := db.Query("SELECT * FROM users WHERE id = " + userID)
diff --git a/handler.go b/handler.go
index 83db48f..2c47bf9 100644
--- a/handler.go
+++ b/handler.go
@@ -5,2 +6,3 @@
 func Handle(data []byte) {
+	var result map[string]string
+	json.Unmarshal(data, &result)
`
}

// recordingAgent is a review agent that records the input it received and
// turns matching preFindings into findings that cite their IDs — standing
// in for the real LLM agents whose prompt injection was covered in the
// agents package tests.
type recordingAgent struct {
	name string

	mu  sync.Mutex
	ins []agents.ReviewInput
}

func (a *recordingAgent) Name() string { return a.name }

func (a *recordingAgent) Review(_ context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
	a.mu.Lock()
	a.ins = append(a.ins, input)
	a.mu.Unlock()

	out := make([]agents.AgentFinding, 0, len(input.PreFindings))
	for _, pf := range input.PreFindings {
		out = append(out, agents.AgentFinding{
			Agent:       a.name,
			Severity:    pf.Severity,
			FilePath:    pf.FilePath,
			LineNumber:  pf.Line,
			Message:     "preFinding " + pf.ID + " verified by " + a.name + ": " + pf.Message,
			CodeSnippet: pf.Snippet,
			Suggestion:  "apply deterministic fix",
			Confidence:  0.95,
		})
	}
	return out, nil
}

// passthroughVerifier mimics a verifier that confirms every finding.
type passthroughVerifier struct{}

func (passthroughVerifier) Verify(_ context.Context, findings []agents.AgentFinding, _ agents.ReviewInput) ([]agents.VerifiedFinding, error) {
	out := make([]agents.VerifiedFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, agents.VerifiedFinding{AgentFinding: f, Confidence: f.Confidence})
	}
	return out, nil
}

// TestE2EMixedProblemPR is the T21 deliverable: a PR mixing a hardcoded
// key, a SQL injection and an unhandled error must produce preFindings for
// all three classes, inject them into every agent, and yield final
// findings citing the preFinding IDs (AC-32 ~ AC-35, AC-41, AC-42).
func TestE2EMixedProblemPR(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test runs real semgrep/golangci-lint CLIs; skipped in -short mode")
	}
	root, rulesDir := e2eFixtureModule(t)
	diffText := e2eDiffText()
	logger := zap.NewNop()

	cfg := config.StaticAnalysisConfig{
		Enabled: true,
		// Workers 1 (serial): golangci-lint enforces a single instance per
		// workspace ("parallel golangci-lint is running"), so concurrent
		// same-package jobs would fail-fast. Pipeline parallelism is
		// covered by the pipeline unit tests.
		Workers: 1,
		// Generous timeout: under load semgrep's cold start (Python
		// interpreter + rule parse) can exceed 30s on CI-like machines.
		PerFileTimeoutSeconds: 180,
		SemgrepRulesDir:       "rules",
		Tools: config.StaticAnalysisTools{
			Semgrep:      config.StaticToolToggle{Enabled: true},
			GolangciLint: config.StaticToolToggle{Enabled: true},
			Gitleaks: config.StaticGitleaksConfig{
				Enabled: true,
				CustomRules: []config.StaticGitleaksRule{{
					ID:          "openai-style-key",
					Description: "OpenAI-style API key",
					Pattern:     `sk-[A-Za-z0-9]+`,
					Keywords:    []string{"sk-"},
				}},
			},
		},
	}
	require.DirExists(t, rulesDir, "semgrep rules must be staged in the workspace")

	// Stage 1: the real static analysis pipeline over the fixture module.
	files, err := diff.Parse(diffText)
	require.NoError(t, err, "parse diff")
	require.Len(t, files, 3)

	// The real orchestrator with mock agents + verifier; its internal
	// pre-analysis pipeline is the single source of preFindings (no
	// separate pipeline run: golangci-lint's per-workspace lock makes
	// duplicate concurrent runs flaky).
	security := &recordingAgent{name: "security"}
	logic := &recordingAgent{name: "logic"}
	orch := orchestrator.New(logger, root, []agents.ReviewAgent{security, logic},
		func(string) orchestrator.Verifier { return passthroughVerifier{} }, nil)
	orch.SetAnalysisConfig(cfg)

	res, err := orch.Run(context.Background(), orchestrator.Input{
		RepoFullName:  "owner/repo",
		PRNumber:      1,
		HeadSHA:       "abc123",
		DiffText:      diffText,
		WorkspaceRoot: root,
	})
	require.NoError(t, err, "orchestrator.Run")
	for tool, msg := range res.AgentErrors {
		t.Logf("agent error: %s: %s", tool, msg)
	}

	preFindings := security.ins[0].PreFindings
	byTool := map[string][]analysis.PreAnalysisFinding{}
	for _, pf := range preFindings {
		byTool[pf.Tool] = append(byTool[pf.Tool], pf)
	}
	// AC-32: gitleaks critical for the hardcoded key.
	require.NotEmpty(t, byTool["gitleaks"], "no gitleaks findings: %+v", preFindings)
	assert.Equal(t, "critical", byTool["gitleaks"][0].Severity)
	// AC-33: semgrep critical SQL injection.
	require.NotEmpty(t, byTool["semgrep"], "no semgrep findings: %+v", preFindings)
	assert.Equal(t, "critical", byTool["semgrep"][0].Severity)
	assert.Contains(t, byTool["semgrep"][0].RuleID, "sql-injection")
	// AC-34: golangci-lint warning for the unhandled json.Unmarshal error.
	require.NotEmpty(t, byTool["golangci-lint"], "no golangci-lint findings: %+v", preFindings)
	var errcheckFound bool
	for _, pf := range byTool["golangci-lint"] {
		if pf.RuleID == "errcheck" && pf.FilePath == "handler.go" {
			errcheckFound = true
			assert.Equal(t, "warning", pf.Severity)
			assert.Contains(t, pf.Message, "json.Unmarshal")
		}
	}
	assert.True(t, errcheckFound, "missing errcheck warning for handler.go: %+v", byTool["golangci-lint"])
	// All preFindings carry stable `<tool>:<ruleID>:<file>:<line>` IDs.
	for _, pf := range preFindings {
		assert.Equal(t,
			fmt.Sprintf("%s:%s:%s:%d", pf.Tool, pf.RuleID, pf.FilePath, pf.Line), pf.ID)
	}

	// Every agent must have received identical preFindings (injection
	// works, AC-35).
	require.Len(t, logic.ins, 1, "agent logic must be invoked exactly once")
	require.Len(t, logic.ins[0].PreFindings, len(preFindings))
	assert.Len(t, security.ins[0].Diffs, 3)
	// The run result echoes the preFindings for persistence (AC-43).
	require.Len(t, res.PreFindings, len(preFindings))

	// AC-41: final findings must cite preFinding IDs. Both mock agents
	// report every preFinding, hence 2x.
	require.Len(t, res.Findings, 2*len(preFindings))
	ids := map[string]bool{}
	for _, pf := range preFindings {
		ids[pf.ID] = true
	}
	for _, f := range res.Findings {
		assert.True(t, strings.Contains(f.Message, "preFinding "),
			"finding must cite a preFinding: %s", f.Message)
		cited := strings.SplitN(strings.TrimPrefix(f.Message, "preFinding "), " ", 2)[0]
		assert.True(t, ids[cited], "cited ID %q must exist in preFindings", cited)
	}
}

// TestE2EDegradedAllToolsDisabled verifies the degradation path (AC-40):
// when every static analysis tool is disabled the pipeline returns no
// preFindings and the agents degrade to pure LLM review.
func TestE2EDegradedAllToolsDisabled(t *testing.T) {
	root, _ := e2eFixtureModule(t)
	diffText := e2eDiffText()
	logger := zap.NewNop()

	cfg := config.StaticAnalysisConfig{Enabled: false}
	pipeline := analysis.NewPipeline(logger, root, cfg)
	res, err := pipeline.Run(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, res.ByFile)
	assert.Empty(t, res.Failures)

	agent := &recordingAgent{name: "security"}
	orch := orchestrator.New(logger, root, []agents.ReviewAgent{agent},
		func(string) orchestrator.Verifier { return passthroughVerifier{} }, nil)
	orch.SetAnalysisConfig(cfg)

	runRes, err := orch.Run(context.Background(), orchestrator.Input{
		RepoFullName:  "owner/repo",
		PRNumber:      2,
		HeadSHA:       "def456",
		DiffText:      diffText,
		WorkspaceRoot: root,
	})
	require.NoError(t, err)
	require.Len(t, agent.ins, 1)
	assert.Empty(t, agent.ins[0].PreFindings, "disabled pipeline must inject zero preFindings")
	assert.Empty(t, runRes.Findings)
	assert.Less(t, runRes.Duration, 30*time.Second)
}

// e2eContextModule creates a Go module whose exported function signature
// was changed and that has three call sites, returning its root and the
// unified diff of the change.
func e2eContextModule(t *testing.T) (root, diffText string) {
	t.Helper()
	root = t.TempDir()
	files := map[string]string{
		"go.mod":  "module e2ectx\n\ngo 1.26\n",
		"calc.go": "package e2ectx\n\nfunc CalculateTotal(items []int, tax float64) int {\n\treturn len(items)\n}\n",
		"callers.go": "package e2ectx\n\n" +
			"func A() int { return CalculateTotal(nil, 0) }\n\n" +
			"func B() int { return CalculateTotal(nil, 0) }\n\n" +
			"func C() int { return CalculateTotal(nil, 0) }\n",
	}
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(content), 0o644), "write %s", name)
	}
	diffText = `diff --git a/calc.go b/calc.go
index 83db48f..2c47bf9 100644
--- a/calc.go
+++ b/calc.go
@@ -1,5 +1,5 @@
 package e2ectx
 
-func CalculateTotal(items []int) int {
+func CalculateTotal(items []int, tax float64) int {
 	return len(items)
 }
`
	return root, diffText
}

// TestE2EContextInjectionOnSignatureChange is the v1.1 T28 deliverable: a
// PR changing an exported function signature must trigger the real
// ContextBuilder, and the retrieved call sites must be injected into the
// agent's ReviewInput (AC-52, AC-55, AC-56).
func TestE2EContextInjectionOnSignatureChange(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test runs the real go/packages loader; skipped in -short mode")
	}
	root, diffText := e2eContextModule(t)
	logger := zap.NewNop()

	agent := &recordingAgent{name: "security"}
	orch := orchestrator.New(logger, root, []agents.ReviewAgent{agent},
		func(string) orchestrator.Verifier { return passthroughVerifier{} }, nil)
	// Skip static analysis: this test exercises the context builder only.
	orch.SetAnalysisConfig(config.StaticAnalysisConfig{Enabled: false})
	// Generous load timeout: the production default is 10s, but parallel
	// test runs can slow the go/packages load beyond that.
	orch.SetContextBuilderFactory(func(wsRoot string) orchestrator.ContextBuilder {
		b := ctxcontext.NewBuilder(wsRoot, logger)
		b.LoadTimeout = 60 * time.Second
		return b
	})

	if _, err := orch.Run(context.Background(), orchestrator.Input{
		RepoFullName:  "owner/repo",
		PRNumber:      3,
		HeadSHA:       "ctxsha",
		DiffText:      diffText,
		WorkspaceRoot: root,
	}); err != nil {
		t.Fatalf("orchestrator.Run: %v", err)
	}

	require.Len(t, agent.ins, 1, "agent must be invoked once")
	bundle := agent.ins[0].Context

	require.Len(t, bundle.Changes, 1, "structural change not detected: %+v", bundle.Changes)
	assert.Equal(t, "CalculateTotal", bundle.Changes[0].FunctionName)
	assert.Equal(t, "signature_changed", bundle.Changes[0].ChangeType)

	require.Len(t, bundle.Callers, 3, "call sites not injected: %+v", bundle.Callers)
	for _, c := range bundle.Callers {
		assert.Equal(t, "callers.go", c.FilePath)
		assert.Positive(t, c.Line)
		assert.Contains(t, c.Snippet, "CalculateTotal")
		assert.NotEmpty(t, c.CallerFunc)
	}
}

// stubAgent counts invocations and returns one canned finding so the
// verifier has something to verify (v1.2 strategy tests).
type stubAgent struct {
	name string
	mu   sync.Mutex
	ins  []agents.ReviewInput
}

func (s *stubAgent) Name() string { return s.name }

func (s *stubAgent) Review(_ context.Context, in agents.ReviewInput) ([]agents.AgentFinding, error) {
	s.mu.Lock()
	s.ins = append(s.ins, in)
	s.mu.Unlock()
	file := "f.go"
	if len(in.Diffs) > 0 && in.Diffs[0].Path != "" {
		file = in.Diffs[0].Path
	}
	return []agents.AgentFinding{{
		Agent: s.name, Severity: "warning", FilePath: file, LineNumber: 1,
		Message: "stub finding", CodeSnippet: "x", Confidence: 0.5,
	}}, nil
}

func (s *stubAgent) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ins)
}

// countingVerifier counts verification calls and accepts every finding.
type countingVerifier struct {
	mu    sync.Mutex
	calls int
}

func (v *countingVerifier) Verify(_ context.Context, findings []agents.AgentFinding, _ agents.ReviewInput) ([]agents.VerifiedFinding, error) {
	v.mu.Lock()
	v.calls++
	v.mu.Unlock()
	out := make([]agents.VerifiedFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, agents.VerifiedFinding{AgentFinding: f, Confidence: 0.9})
	}
	return out, nil
}

// TestE2EStrategySwitching is the v1.2 T37 deliverable: the real
// deterministic assessor + selector must switch the agent set based on the
// dataset samples (AC-74, AC-76).
func TestE2EStrategySwitching(t *testing.T) {
	tests := []struct {
		name         string
		sampleDir    string
		wantLevel    review.ComplexityLevel
		wantAgents   []string
		wantVerifier bool
	}{
		{
			name:         "trivial sample: 5-line diff, 1 file => only security+lint, no verifier",
			sampleDir:    filepath.Join("..", "..", "eval", "dataset", "sample-trivial"),
			wantLevel:    review.ComplexityTrivial,
			wantAgents:   []string{"security", "lint"},
			wantVerifier: false,
		},
		{
			name:         "complex sample: 300-line diff => all 4 agents + verifier",
			sampleDir:    filepath.Join("..", "..", "eval", "dataset", "sample-complex"),
			wantLevel:    review.ComplexityComplex,
			wantAgents:   []string{"security", "logic", "performance", "lint"},
			wantVerifier: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diffText, err := os.ReadFile(filepath.Join(tc.sampleDir, "pr.diff"))
			require.NoError(t, err, "read sample diff")

			logger := zap.NewNop()
			names := []string{"security", "logic", "performance", "lint"}
			agentsByName := map[string]*stubAgent{}
			reviewAgents := make([]agents.ReviewAgent, 0, len(names))
			for _, n := range names {
				a := &stubAgent{name: n}
				agentsByName[n] = a
				reviewAgents = append(reviewAgents, a)
			}
			verifier := &countingVerifier{}
			orch := orchestrator.New(logger, t.TempDir(), reviewAgents,
				func(string) orchestrator.Verifier { return verifier }, nil)
			// Skip static analysis: this test exercises the strategy only.
			orch.SetAnalysisConfig(config.StaticAnalysisConfig{Enabled: false})

			res, err := orch.Run(context.Background(), orchestrator.Input{
				RepoFullName:  "owner/repo",
				PRNumber:      11,
				HeadSHA:       "strategy-" + tc.name,
				DiffText:      string(diffText),
				WorkspaceRoot: t.TempDir(),
			})
			require.NoError(t, err, "orchestrator.Run")

			assert.Equal(t, tc.wantLevel, res.ComplexityLevel, "complexity level")

			enabled := map[string]bool{}
			for _, n := range tc.wantAgents {
				enabled[n] = true
			}
			for _, n := range names {
				got := agentsByName[n].callCount()
				if enabled[n] {
					assert.Equal(t, 1, got, "agent %s should run once", n)
					assert.Equal(t, "executed", res.AgentStatus[n])
				} else {
					assert.Equal(t, 0, got, "agent %s must be skipped", n)
					assert.Equal(t, "skipped_by_strategy", res.AgentStatus[n])
				}
			}

			if tc.wantVerifier {
				assert.Equal(t, 1, verifier.calls, "verifier should run")
			} else {
				assert.Equal(t, 0, verifier.calls, "verifier must be skipped")
				// Skipped verifier accepts raw findings with full confidence.
				require.NotEmpty(t, res.Findings)
				for _, f := range res.Findings {
					assert.Equal(t, 1.0, f.Confidence, "skipped verifier sets confidence 1.0")
				}
			}
		})
	}
}
