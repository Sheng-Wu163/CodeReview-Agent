package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ltools "github.com/tmc/langchaingo/tools"

	"codereviewagent/internal/ai/tools"
	"codereviewagent/internal/diff"
)

// verifierWorkspace creates a workspace with one real source file whose
// content the test findings reference.
func verifierWorkspace(t *testing.T) (root string, readFile ltools.Tool) {
	t.Helper()
	root = t.TempDir()
	content := "package db\n\nfunc GetUser(userID string) (*User, error) {\n" +
		"\trows, err := db.Query(\"SELECT * FROM users WHERE id = \" + userID)\n" +
		"\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn nil, nil\n}\n"
	if err := os.WriteFile(filepath.Join(root, "users.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, tools.ReadFileTool{Root: root}
}

// verifierDiff builds a diff covering users.go lines 3-4 (new numbering).
func verifierDiff() []diff.FileDiff {
	return []diff.FileDiff{
		{
			Path:       "users.go",
			ChangeType: diff.ChangeModified,
			Hunks: []diff.Hunk{
				{
					OldStartLine: 3, OldLines: 2,
					NewStartLine: 3, NewLines: 2,
					Lines: []diff.Line{
						{Type: diff.LineContext, Content: "func GetUser(userID string) (*User, error) {", OldNumber: 3, NewNumber: 3},
						{Type: diff.LineAdded, Content: "\trows, err := db.Query(\"SELECT * FROM users WHERE id = \" + userID)", NewNumber: 4},
					},
				},
			},
		},
	}
}

func TestVerifierFiltersHallucinationsAndDedupes(t *testing.T) {
	_, readFile := verifierWorkspace(t)
	v := NewVerifier(readFile, nil)

	findings := []AgentFinding{
		{
			Agent: "security", Severity: SeverityCritical,
			FilePath: "users.go", LineNumber: 4,
			Message: "SQL injection", CodeSnippet: `db.Query("SELECT * FROM users WHERE id = " + userID)`,
			Confidence: 0.9,
		},
		{
			// Hallucination: snippet does not exist in users.go (AC-08).
			Agent: "security", Severity: SeverityCritical,
			FilePath: "users.go", LineNumber: 4,
			Message: "made up issue", CodeSnippet: "this line does not exist anywhere",
			Confidence: 0.95,
		},
		{
			// Duplicate: same problem, same line, same severity, other agent
			// (AC-09).
			Agent: "logic", Severity: SeverityCritical,
			FilePath: "users.go", LineNumber: 4,
			Message: "SQL injection via concatenation", CodeSnippet: `db.Query("SELECT * FROM users WHERE id = " + userID)`,
			Confidence: 0.85,
		},
		{
			// Out-of-diff line: dropped by line validation (FR-10 step 1).
			Agent: "lint", Severity: SeveritySuggestion,
			FilePath: "users.go", LineNumber: 99,
			Message:     "far outside the diff",
			CodeSnippet: "return nil, nil",
			Confidence:  0.8,
		},
	}

	verified, err := v.Verify(context.Background(), findings, ReviewInput{Diffs: verifierDiff()})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if len(verified) != 1 {
		t.Fatalf("verified = %d findings (%+v), want 1", len(verified), verified)
	}
	got := verified[0]
	if got.Agent != "security" {
		t.Errorf("kept finding Agent = %q, want security (higher confidence variant)", got.Agent)
	}
	if got.Message != "SQL injection" {
		t.Errorf("kept finding Message = %q, want the higher-confidence variant", got.Message)
	}
	if got.Confidence <= 0.9 || got.Confidence > 1 {
		t.Errorf("Confidence = %f, want boosted above 0.9 by corroboration and clamped to <=1", got.Confidence)
	}
}

func TestVerifierLineOutOfRangeDropped(t *testing.T) {
	_, readFile := verifierWorkspace(t)
	v := NewVerifier(readFile, nil)

	findings := []AgentFinding{
		{Agent: "logic", Severity: SeverityWarning, FilePath: "users.go", LineNumber: 100,
			Message: "outside diff", CodeSnippet: "", Confidence: 0.9},
		{Agent: "logic", Severity: SeverityWarning, FilePath: "users.go", LineNumber: 3,
			Message: "inside diff", CodeSnippet: "", Confidence: 0.9},
		{Agent: "logic", Severity: SeverityWarning, FilePath: "not_in_diff.go", LineNumber: 1,
			Message: "other file", CodeSnippet: "", Confidence: 0.9},
	}

	verified, err := v.Verify(context.Background(), findings, ReviewInput{Diffs: verifierDiff()})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(verified) != 1 || verified[0].Message != "inside diff" {
		t.Fatalf("verified = %+v, want only the in-diff finding", verified)
	}
}

func TestVerifierEmptySnippetKeptWithoutBonus(t *testing.T) {
	_, readFile := verifierWorkspace(t)
	v := NewVerifier(readFile, nil)

	findings := []AgentFinding{
		{Agent: "performance", Severity: SeverityWarning, FilePath: "users.go", LineNumber: 4,
			Message: "query in hot path", CodeSnippet: "", Confidence: 0.5},
	}
	verified, err := v.Verify(context.Background(), findings, ReviewInput{Diffs: verifierDiff()})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(verified) != 1 {
		t.Fatalf("verified = %d, want 1 (empty snippet is not a hallucination)", len(verified))
	}
	if verified[0].Confidence < 0.5 || verified[0].Confidence > 0.51 {
		t.Errorf("Confidence = %f, want unchanged base 0.5 (no snippet bonus)", verified[0].Confidence)
	}
}

func TestVerifierMissingFileDropped(t *testing.T) {
	_, readFile := verifierWorkspace(t)
	v := NewVerifier(readFile, nil)

	findings := []AgentFinding{
		{Agent: "security", Severity: SeverityCritical, FilePath: "ghost.go", LineNumber: 1,
			Message: "invented file", CodeSnippet: "var x = 1", Confidence: 0.9},
	}
	verified, err := v.Verify(context.Background(), findings, ReviewInput{Diffs: verifierDiff()})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(verified) != 0 {
		t.Errorf("verified = %+v, want empty (snippet in non-existent file is a hallucination)", verified)
	}
}

func TestVerifierLLMConfidenceBlend(t *testing.T) {
	_, readFile := verifierWorkspace(t)

	// Mock LLM scores every finding 1.0; deterministic score is ~0.95+0.1
	// clamped to 1.0, so blended result must stay 1.0. A second case with
	// empty snippet (base 0.5) blends to 0.75.
	mock := &mockLLM{response: "1.0"}
	v := NewVerifier(readFile, mock)

	findings := []AgentFinding{
		{Agent: "performance", Severity: SeverityWarning, FilePath: "users.go", LineNumber: 4,
			Message: "query in hot path", CodeSnippet: "", Confidence: 0.5},
	}
	verified, err := v.Verify(context.Background(), findings, ReviewInput{Diffs: verifierDiff()})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(verified) != 1 {
		t.Fatalf("verified = %d, want 1", len(verified))
	}
	if verified[0].Confidence < 0.74 || verified[0].Confidence > 0.76 {
		t.Errorf("Confidence = %f, want blended 0.75", verified[0].Confidence)
	}
	if !strings.Contains(mock.lastPrompt, "VERIFICATION reviewer") {
		t.Errorf("LLM prompt must embed verifier.txt system prompt:\n%.300s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "query in hot path") {
		t.Errorf("LLM prompt must embed the finding:\n%.300s", mock.lastPrompt)
	}
}

func TestVerifierLLMBadScoreErrors(t *testing.T) {
	_, readFile := verifierWorkspace(t)
	mock := &mockLLM{response: "not a number"}
	v := NewVerifier(readFile, mock)

	findings := []AgentFinding{
		{Agent: "lint", Severity: SeveritySuggestion, FilePath: "users.go", LineNumber: 3,
			Message: "naming", CodeSnippet: "", Confidence: 0.5},
	}
	if _, err := v.Verify(context.Background(), findings, ReviewInput{Diffs: verifierDiff()}); err == nil {
		t.Error("unparseable LLM confidence should produce an error")
	}
}

func TestContainsNormalized(t *testing.T) {
	tests := []struct {
		s, needle string
		want      bool
	}{
		{"if err != nil {\n\treturn\n}", "if err != nil { return }", true},
		{"\trows, err := db.Query(x)", "rows, err := db.Query(x)", true},
		{"package db", "package main", false},
		{"", "x", false},
	}
	for _, tt := range tests {
		if got := containsNormalized(tt.s, tt.needle); got != tt.want {
			t.Errorf("containsNormalized(%q, %q) = %v, want %v", tt.s, tt.needle, got, tt.want)
		}
	}
}
