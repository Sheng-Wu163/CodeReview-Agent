package agents

import (
	"context"
	"strings"
	"testing"

	"codereviewagent/internal/diff"
)

// stringConcatDiff builds a FileDiff containing string += inside a loop
// (AC-08 in checklist: 循环内字符串拼接).
func stringConcatDiff() []diff.FileDiff {
	return []diff.FileDiff{
		{
			Path:       "internal/report/render.go",
			ChangeType: diff.ChangeModified,
			Hunks: []diff.Hunk{
				{
					OldStartLine: 5, OldLines: 4,
					NewStartLine: 5, NewLines: 7,
					Lines: []diff.Line{
						{Type: diff.LineContext, Content: "func Render(items []string) string {", OldNumber: 5, NewNumber: 5},
						{Type: diff.LineAdded, Content: "	s := \"\"", NewNumber: 6},
						{Type: diff.LineAdded, Content: "	for _, item := range items {", NewNumber: 7},
						{Type: diff.LineAdded, Content: "		s += item + \",\"", NewNumber: 8},
						{Type: diff.LineAdded, Content: "	}", NewNumber: 9},
						{Type: diff.LineAdded, Content: "	return s", NewNumber: 10},
						{Type: diff.LineContext, Content: "}", OldNumber: 6, NewNumber: 11},
					},
				},
			},
		},
	}
}

func TestPerformanceAgentDetectsStringConcatInLoop(t *testing.T) {
	mock := &mockLLM{
		response: "Thought: I now know the final answer\nFinal Answer: " +
			`[{"agent":"performance","severity":"warning","file_path":"internal/report/render.go","line_number":8,` +
			`"message":"String concatenation with += inside a loop copies the string every iteration, O(n^2)",` +
			`"code_snippet":"s += item + \",\"",` +
			`"suggestion":"Use strings.Builder and its WriteString method inside the loop","confidence":0.9}]`,
	}
	agent := NewPerformanceAgent(mock)

	findings, err := agent.Review(context.Background(), stringConcatDiff(), securityAgentTools(t))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	if len(findings) == 0 {
		t.Fatal("expected at least 1 finding for string concatenation in loop")
	}
	var warning int
	for _, f := range findings {
		if f.Severity == SeverityWarning {
			warning++
		}
	}
	if warning == 0 {
		t.Errorf("expected at least 1 warning finding, got %v", severitiesOf(findings))
	}
	f := findings[0]
	if f.Agent != "performance" {
		t.Errorf("Agent = %q, want performance", f.Agent)
	}
	if f.FilePath != "internal/report/render.go" || f.LineNumber != 8 {
		t.Errorf("location = %s:%d, want internal/report/render.go:8", f.FilePath, f.LineNumber)
	}
	if !strings.Contains(f.Suggestion, "strings.Builder") {
		t.Errorf("Suggestion should mention strings.Builder, got %q", f.Suggestion)
	}
}

func TestPerformanceAgentPromptContent(t *testing.T) {
	mock := &mockLLM{response: "Final Answer: []"}
	agent := NewPerformanceAgent(mock)

	if _, err := agent.Review(context.Background(), stringConcatDiff(), securityAgentTools(t)); err != nil {
		t.Fatalf("Review: %v", err)
	}

	// System prompt must cover the FR-07 problem classes and include the diff.
	if !strings.Contains(mock.lastPrompt, "N+1") {
		t.Errorf("prompt missing N+1 coverage:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "strings.Builder") {
		t.Errorf("prompt missing string-building coverage:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "s += item") {
		t.Errorf("prompt missing changed line:\n%.500s", mock.lastPrompt)
	}
}

func TestPerformanceAgentNoFindings(t *testing.T) {
	mock := &mockLLM{response: "Thought: fine\nFinal Answer: []"}
	agent := NewPerformanceAgent(mock)

	findings, err := agent.Review(context.Background(), stringConcatDiff(), securityAgentTools(t))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %v, want empty", findings)
	}
}

func TestPerformanceAgentImplementsReviewAgentInterface(t *testing.T) {
	var _ ReviewAgent = NewPerformanceAgent(&mockLLM{})
	if NewPerformanceAgent(&mockLLM{}).Name() != "performance" {
		t.Error("performance agent name mismatch")
	}
}
