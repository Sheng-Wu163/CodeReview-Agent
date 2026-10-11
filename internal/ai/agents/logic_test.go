package agents

import (
	"context"
	"strings"
	"testing"

	"codereviewagent/internal/diff"
)

// unhandledErrorDiff builds a FileDiff containing json.Unmarshal whose error
// is ignored (AC-07).
func unhandledErrorDiff() []diff.FileDiff {
	return []diff.FileDiff{
		{
			Path:       "internal/api/decode.go",
			ChangeType: diff.ChangeAdded,
			Hunks: []diff.Hunk{
				{
					OldStartLine: 0, OldLines: 0,
					NewStartLine: 1, NewLines: 6,
					Lines: []diff.Line{
						{Type: diff.LineAdded, Content: "func Decode(data []byte) Result {", NewNumber: 1},
						{Type: diff.LineAdded, Content: "	var result Result", NewNumber: 2},
						{Type: diff.LineAdded, Content: "	json.Unmarshal(data, &result)", NewNumber: 3},
						{Type: diff.LineAdded, Content: "	return result", NewNumber: 4},
						{Type: diff.LineAdded, Content: "}", NewNumber: 5},
					},
				},
			},
		},
	}
}

func TestLogicAgentDetectsUnhandledError(t *testing.T) {
	mock := &mockLLM{
		response: "Thought: I now know the final answer\nFinal Answer: " +
			`[{"agent":"logic","severity":"warning","file_path":"internal/api/decode.go","line_number":3,` +
			`"message":"json.Unmarshal error return value is ignored; decoding failures are silent",` +
			`"code_snippet":"json.Unmarshal(data, &result)",` +
			`"suggestion":"Check the returned error: if err := json.Unmarshal(data, &result); err != nil { return err }","confidence":0.9}]`,
	}
	agent := NewLogicAgent(mock)

	findings, err := agent.Review(context.Background(), unhandledErrorDiff(), securityAgentTools(t))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	if len(findings) == 0 {
		t.Fatal("expected at least 1 finding for unhandled json.Unmarshal error (AC-07)")
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
	if f.Agent != "logic" {
		t.Errorf("Agent = %q, want logic", f.Agent)
	}
	if f.FilePath != "internal/api/decode.go" || f.LineNumber != 3 {
		t.Errorf("location = %s:%d, want internal/api/decode.go:3", f.FilePath, f.LineNumber)
	}
	if !strings.Contains(f.Suggestion, "err") {
		t.Errorf("Suggestion should mention error handling, got %q", f.Suggestion)
	}
}

func TestLogicAgentPromptContent(t *testing.T) {
	mock := &mockLLM{response: "Final Answer: []"}
	agent := NewLogicAgent(mock)

	if _, err := agent.Review(context.Background(), unhandledErrorDiff(), securityAgentTools(t)); err != nil {
		t.Fatalf("Review: %v", err)
	}

	// System prompt must cover the FR-06 problem classes and the diff must
	// be included.
	if !strings.Contains(mock.lastPrompt, "json.Unmarshal") {
		t.Errorf("prompt missing unhandled-error coverage:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "goroutine") {
		t.Errorf("prompt missing goroutine-leak coverage:\n%.500s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "json.Unmarshal(data, &result)") {
		t.Errorf("prompt missing changed line:\n%.500s", mock.lastPrompt)
	}
}

func TestLogicAgentEmptyFindings(t *testing.T) {
	mock := &mockLLM{response: "Thought: clean\nFinal Answer: []"}
	agent := NewLogicAgent(mock)

	findings, err := agent.Review(context.Background(), unhandledErrorDiff(), securityAgentTools(t))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %v, want empty", findings)
	}
}

func TestLogicAgentImplementsReviewAgentInterface(t *testing.T) {
	var _ ReviewAgent = NewLogicAgent(&mockLLM{})
	if NewLogicAgent(&mockLLM{}).Name() != "logic" {
		t.Error("logic agent name mismatch")
	}
}
