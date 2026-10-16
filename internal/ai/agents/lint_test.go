package agents

import (
	"context"
	"strings"
	"testing"

	"codereviewagent/internal/diff"
)

// badNamingDiff builds a FileDiff containing a Go naming convention
// violation (AC-09: 不符合 Go 命名规范的函数名).
func badNamingDiff() []diff.FileDiff {
	return []diff.FileDiff{
		{
			Path:       "internal/service/user_service.go",
			ChangeType: diff.ChangeModified,
			Hunks: []diff.Hunk{
				{
					OldStartLine: 3, OldLines: 3,
					NewStartLine: 3, NewLines: 5,
					Lines: []diff.Line{
						{Type: diff.LineContext, Content: "func fetchUser(id int) (*User, error) {", OldNumber: 3, NewNumber: 3},
						{Type: diff.LineAdded, Content: "func Get_User_Data(id int) (*User, error) {", NewNumber: 4},
						{Type: diff.LineAdded, Content: "	return fetchUser(id)", NewNumber: 5},
						{Type: diff.LineAdded, Content: "}", NewNumber: 6},
						{Type: diff.LineContext, Content: "}", OldNumber: 4, NewNumber: 7},
					},
				},
			},
		},
	}
}

func TestLintAgentDetectsBadNaming(t *testing.T) {
	mock := &mockLLM{
		response: "Thought: I now know the final answer\nFinal Answer: " +
			`[{"agent":"lint","severity":"suggestion","file_path":"internal/service/user_service.go","line_number":4,` +
			`"message":"Get_User_Data does not follow Go naming conventions (mixedCaps, no underscores)",` +
			`"code_snippet":"func Get_User_Data(id int) (*User, error) {",` +
			`"suggestion":"Rename to getUserData (or GetUserData if it must be exported)","confidence":0.85}]`,
	}
	agent := NewLintAgent(mock)

	findings, err := agent.Review(context.Background(), reviewInput(t, badNamingDiff(), nil))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	if len(findings) == 0 {
		t.Fatal("expected at least 1 finding for naming violation (AC-09)")
	}
	var suggestion int
	for _, f := range findings {
		if f.Severity == SeveritySuggestion {
			suggestion++
		}
	}
	if suggestion == 0 {
		t.Errorf("expected at least 1 suggestion finding, got %v", severitiesOf(findings))
	}
	f := findings[0]
	if f.Agent != "lint" {
		t.Errorf("Agent = %q, want lint", f.Agent)
	}
	if f.FilePath != "internal/service/user_service.go" || f.LineNumber != 4 {
		t.Errorf("location = %s:%d, want internal/service/user_service.go:4", f.FilePath, f.LineNumber)
	}
}

func TestLintAgentPromptInstructsStaticAnalysisFirst(t *testing.T) {
	mock := &mockLLM{response: "Final Answer: []"}
	agent := NewLintAgent(mock)

	if _, err := agent.Review(context.Background(), reviewInput(t, badNamingDiff(), nil)); err != nil {
		t.Fatalf("Review: %v", err)
	}

	// v1.3 (FR-22): the linter findings come from the preFindings section;
	// the system prompt must instruct verification + supplementation.
	if !strings.Contains(mock.lastPrompt, "## 静态分析发现") {
		t.Errorf("prompt missing '## 静态分析发现' section:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "Naming conventions") {
		t.Errorf("prompt missing naming coverage:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "Get_User_Data") {
		t.Errorf("prompt missing changed line:\n%.500s", mock.lastPrompt)
	}
}

func TestLintAgentNoFindings(t *testing.T) {
	mock := &mockLLM{response: "Thought: clean\nFinal Answer: []"}
	agent := NewLintAgent(mock)

	findings, err := agent.Review(context.Background(), reviewInput(t, badNamingDiff(), nil))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %v, want empty", findings)
	}
}

func TestLintAgentImplementsReviewAgentInterface(t *testing.T) {
	var _ ReviewAgent = NewLintAgent(&mockLLM{})
	if NewLintAgent(&mockLLM{}).Name() != "lint" {
		t.Error("lint agent name mismatch")
	}
}
