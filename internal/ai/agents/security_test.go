package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
	ltools "github.com/tmc/langchaingo/tools"

	"codereviewagent/internal/ai/tools"
	"codereviewagent/internal/analysis"
	"codereviewagent/internal/diff"
)

// mockLLM is a scripted llms.Model for testing the ReAct loop. It records
// the last prompt it received and replies with a fixed response.
type mockLLM struct {
	response   string
	err        error
	lastPrompt string
	calls      int
}

// Call implements llms.Model (deprecated interface, delegates).
func (m *mockLLM) Call(_ context.Context, prompt string, _ ...llms.CallOption) (string, error) {
	m.lastPrompt = prompt
	m.calls++
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

// GenerateContent implements llms.Model.
func (m *mockLLM) GenerateContent(_ context.Context, messages []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	var prompt strings.Builder
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if tc, ok := part.(llms.TextContent); ok {
				prompt.WriteString(tc.Text)
			}
		}
	}
	m.lastPrompt = prompt.String()
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: m.response}},
	}, nil
}

// sqlInjectionDiff builds a FileDiff list containing the classic string
// concatenation SQL query (AC-06).
func sqlInjectionDiff() []diff.FileDiff {
	return []diff.FileDiff{
		{
			Path:       "internal/db/users.go",
			ChangeType: diff.ChangeModified,
			Hunks: []diff.Hunk{
				{
					OldStartLine: 10, OldLines: 5,
					NewStartLine: 10, NewLines: 6,
					Lines: []diff.Line{
						{Type: diff.LineContext, Content: "func GetUser(userID string) (*User, error) {", OldNumber: 10, NewNumber: 10},
						{Type: diff.LineAdded, Content: `	rows, err := db.Query("SELECT * FROM users WHERE id = " + userID)`, NewNumber: 11},
						{Type: diff.LineAdded, Content: "	if err != nil {", NewNumber: 12},
						{Type: diff.LineContext, Content: "		return nil, err", OldNumber: 11, NewNumber: 13},
					},
				},
			},
		},
	}
}

// securityAgentTools builds the default tool set against a temp workspace.
func securityAgentTools(t *testing.T) []ltools.Tool {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "users.go"), []byte("package db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return []ltools.Tool{
		tools.ReadFileTool{Root: root},
	}
}

// reviewInput builds a ReviewInput for tests, wiring the standard tool set.
func reviewInput(t *testing.T, diffs []diff.FileDiff, pre []analysis.PreAnalysisFinding) ReviewInput {
	return ReviewInput{Diffs: diffs, PreFindings: pre, Tools: securityAgentTools(t)}
}

// TestSecurityAgentPromptContainsPreFindings is the T20 acceptance test:
// given a SQL injection diff plus matching preFindings, the agent prompt
// must contain the "## 静态分析发现" section with the preFinding ID, and the
// returned finding must cite it.
func TestSecurityAgentPromptContainsPreFindings(t *testing.T) {
	preID := "semgrep:sql-injection-concat:internal/db/users.go:11"
	pre := []analysis.PreAnalysisFinding{
		{
			ID: preID, Tool: "semgrep", RuleID: "sql-injection-concat",
			FilePath: "internal/db/users.go", Line: 11, EndLine: 11,
			Severity: "critical", Message: "SQL built by string concatenation",
			Snippet: `db.Query("SELECT * FROM users WHERE id = " + userID)`,
		},
	}
	mock := &mockLLM{
		response: "Thought: I now know the final answer\nFinal Answer: " +
			`[{"agent":"security","severity":"critical","file_path":"internal/db/users.go","line_number":11,` +
			`"message":"preFinding semgrep:sql-injection-concat:internal/db/users.go:11 confirmed: SQL injection via concatenation",` +
			`"code_snippet":"db.Query(\"SELECT * FROM users WHERE id = \" + userID)",` +
			`"suggestion":"parameterize","confidence":0.97}]`,
	}
	agent := NewSecurityAgent(mock)

	findings, err := agent.Review(context.Background(), reviewInput(t, sqlInjectionDiff(), pre))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	// Prompt must carry the mandatory section header and the preFinding ID
	// (FR-22).
	if !strings.Contains(mock.lastPrompt, "## 静态分析发现") {
		t.Errorf("prompt missing '## 静态分析发现' section:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, preID) {
		t.Errorf("prompt missing preFinding ID:\n%.500s", mock.lastPrompt)
	}
	// Agent output must cite the preFinding ID (T20 acceptance).
	if len(findings) == 0 {
		t.Fatal("expected at least 1 finding")
	}
	if !strings.Contains(findings[0].Message, preID) {
		t.Errorf("finding message = %q, want preFinding ID citation", findings[0].Message)
	}
}

func TestSecurityAgentPromptWithoutPreFindings(t *testing.T) {
	mock := &mockLLM{response: "Final Answer: []"}
	agent := NewSecurityAgent(mock)

	if _, err := agent.Review(context.Background(), reviewInput(t, sqlInjectionDiff(), nil)); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !strings.Contains(mock.lastPrompt, "## 静态分析发现") {
		t.Errorf("prompt must always contain the section header:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "(none") {
		t.Errorf("empty preFindings should render a placeholder:\n%.500s", mock.lastPrompt)
	}
}

func TestSecurityAgentDetectsSQLInjection(t *testing.T) {
	mock := &mockLLM{
		response: "Thought: I now know the final answer\nFinal Answer: " +
			`[{"agent":"security","severity":"critical","file_path":"internal/db/users.go","line_number":11,` +
			`"message":"SQL injection: user-controlled userID is concatenated into the query",` +
			`"code_snippet":"db.Query(\"SELECT * FROM users WHERE id = \" + userID)",` +
			`"suggestion":"Use parameterized query db.Query(\"... WHERE id = ?\", userID)","confidence":0.95}]`,
	}
	agent := NewSecurityAgent(mock)

	findings, err := agent.Review(context.Background(), reviewInput(t, sqlInjectionDiff(), nil))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	if len(findings) == 0 {
		t.Fatal("expected at least 1 finding for SQL injection diff (AC-06)")
	}
	var critical int
	for _, f := range findings {
		if f.Severity == SeverityCritical {
			critical++
		}
	}
	if critical == 0 {
		t.Errorf("expected at least 1 critical finding, got severities %v", severitiesOf(findings))
	}
	f := findings[0]
	if f.Agent != "security" {
		t.Errorf("Agent = %q, want security", f.Agent)
	}
	if f.FilePath != "internal/db/users.go" {
		t.Errorf("FilePath = %q, want internal/db/users.go", f.FilePath)
	}
	if f.LineNumber != 11 {
		t.Errorf("LineNumber = %d, want 11", f.LineNumber)
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		t.Errorf("Confidence = %f, want within [0,1]", f.Confidence)
	}
}

func TestSecurityAgentPromptContainsSystemPromptAndDiff(t *testing.T) {
	mock := &mockLLM{response: "Final Answer: []"}
	agent := NewSecurityAgent(mock)

	if _, err := agent.Review(context.Background(), reviewInput(t, sqlInjectionDiff(), nil)); err != nil {
		t.Fatalf("Review: %v", err)
	}

	// The prompt must embed the externalized system prompt (FR: prompts in
	// internal/ai/prompts) and the rendered diff.
	if !strings.Contains(mock.lastPrompt, "SQL injection") {
		t.Errorf("prompt missing system prompt content:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, `db.Query("SELECT * FROM users WHERE id = " + userID)`) {
		t.Errorf("prompt missing the changed line:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "internal/db/users.go") {
		t.Errorf("prompt missing file path:\n%.500s", mock.lastPrompt)
	}
}

func TestSecurityAgentEmptyDiffSkipsLLM(t *testing.T) {
	mock := &mockLLM{response: "Final Answer: []"}
	agent := NewSecurityAgent(mock)

	findings, err := agent.Review(context.Background(), ReviewInput{})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %v, want empty", findings)
	}
	if mock.calls != 0 {
		t.Errorf("LLM called %d times for empty diff, want 0", mock.calls)
	}
}

func TestSecurityAgentMalformedOutput(t *testing.T) {
	mock := &mockLLM{response: "I reviewed the code and it looks mostly fine, no JSON though."}
	agent := NewSecurityAgent(mock)

	_, err := agent.Review(context.Background(), reviewInput(t, sqlInjectionDiff(), nil))
	if err == nil {
		t.Error("malformed LLM output should produce an error")
	}
}

func TestSecurityAgentFindingsJSONWithFences(t *testing.T) {
	// The LLM may wrap the JSON in markdown fences despite instructions; the
	// parser must tolerate it.
	mock := &mockLLM{response: "Thought: I now know the final answer\nFinal Answer: ```json\n" +
		`[{"agent":"security","severity":"WARNING","file_path":"a.go","line_number":3,` +
		`"message":"hardcoded secret","code_snippet":"apiKey = \"sk-1\"","suggestion":"use env var","confidence":2}]` +
		"\n```"}
	agent := NewSecurityAgent(mock)

	findings, err := agent.Review(context.Background(), reviewInput(t, sqlInjectionDiff(), nil))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	if findings[0].Severity != SeverityWarning {
		t.Errorf("Severity = %q, want normalized %q", findings[0].Severity, SeverityWarning)
	}
	if findings[0].Confidence != 1 {
		t.Errorf("Confidence = %f, want clamped to 1", findings[0].Confidence)
	}
}

func TestReviewAgentImplementsReviewAgentInterface(t *testing.T) {
	var _ ReviewAgent = NewSecurityAgent(&mockLLM{})
	if NewSecurityAgent(&mockLLM{}).Name() != "security" {
		t.Error("security agent name mismatch")
	}
}

func TestRenderDiff(t *testing.T) {
	out := renderDiff(sqlInjectionDiff())
	for _, want := range []string{
		"File: internal/db/users.go (modified)",
		"+11\t+",
		"rows, err := db.Query",
		" 10\t ",
		"func GetUser(userID string) (*User, error) {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderDiff missing %q:\n%s", want, out)
		}
	}
}

// severitiesOf collects severities for error messages.
func severitiesOf(fs []AgentFinding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, fmt.Sprintf("%s@%s:%d", f.Severity, f.FilePath, f.LineNumber))
	}
	return out
}
