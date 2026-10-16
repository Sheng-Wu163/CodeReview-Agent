package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codereviewagent/internal/diff"
)

// golangciFixtureJSON is a real golangci-lint v2.14 output captured from a
// module with an unchecked json.Unmarshal error. Kept verbatim to lock the
// parser against format changes (spec 11.5).
const golangciFixtureJSON = `{"Issues":[{"FromLinter":"errcheck","Text":"Error return value of ` + "`json.Unmarshal`" + ` is not checked","Severity":"","SourceLines":["\tjson.Unmarshal(data, \u0026result)"],"Pos":{"Filename":"internal\\decode\\decode.go","Offset":145,"Line":11,"Column":16},"ExpectNoLint":false,"ExpectedNoLintLinter":""},{"FromLinter":"revive","Text":"package-comments: should have a package comment","Severity":"warning","SourceLines":["package decode"],"Pos":{"Filename":"internal\\decode\\decode.go","Offset":0,"Line":1,"Column":1},"LineRange":{"From":1,"To":1},"ExpectNoLint":false,"ExpectedNoLintLinter":""}],"Report":{"Linters":[{"Name":"errcheck","Enabled":true}]}}`

func TestGolangciLintJSONParser(t *testing.T) {
	issues, err := parseGolangciJSON([]byte(golangciFixtureJSON))
	if err != nil {
		t.Fatalf("parseGolangciJSON: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues = %d, want 2", len(issues))
	}

	first := issues[0]
	if first.FromLinter != "errcheck" {
		t.Errorf("FromLinter = %q, want errcheck", first.FromLinter)
	}
	if first.Pos.Filename != `internal\decode\decode.go` {
		t.Errorf("Filename = %q", first.Pos.Filename)
	}
	if first.Pos.Line != 11 {
		t.Errorf("Line = %d, want 11", first.Pos.Line)
	}
	if !strings.Contains(first.Text, "json.Unmarshal") {
		t.Errorf("Text = %q, want unmarshal mention", first.Text)
	}

	second := issues[1]
	if second.LineRange.To != 1 {
		t.Errorf("LineRange.To = %d, want 1", second.LineRange.To)
	}
}

func TestGolangciLintJSONParserInvalid(t *testing.T) {
	if _, err := parseGolangciJSON([]byte("not-json{")); err == nil {
		t.Error("invalid JSON should produce an error")
	}
}

func TestGolangciLintSeverityMapping(t *testing.T) {
	tests := []struct {
		linter string
		want   string
	}{
		{"errcheck", "warning"},
		{"govet", "warning"},
		{"staticcheck", "warning"},
		{"unused", "warning"},
		{"ineffassign", "warning"},
		{"revive", "suggestion"},
		{"gofmt", "suggestion"},
		{"goimports", "suggestion"},
		{"gci", "suggestion"},
		{"misspell", "suggestion"},
	}
	for _, tt := range tests {
		iss := golangciIssue{FromLinter: tt.linter, Pos: struct {
			Filename string `json:"Filename"`
			Line     int    `json:"Line"`
			Column   int    `json:"Column"`
		}{Filename: "a.go", Line: 1, Column: 1}}
		if got := severityFor(iss); got != tt.want {
			t.Errorf("severityFor(%q) = %q, want %q", tt.linter, got, tt.want)
		}
	}
}

// golangciFixtureModule creates a Go module with an unchecked json.Unmarshal
// error and a golangci-lint config enabling errcheck.
func golangciFixtureModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module fixture\n\ngo 1.26\n",
		"internal/decode/decode.go": `package decode

import "encoding/json"

type Result struct {
	Name string
}

// Decode decodes payload without checking the error (errcheck target).
func Decode(data []byte) Result {
	var result Result
	json.Unmarshal(data, &result)
	return result
}
`,
		".golangci.yml": "version: \"2\"\n\nlinters:\n  enable:\n    - errcheck\n    - revive\n",
	}
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGolangciLintAnalyzerDetectsUnhandledError(t *testing.T) {
	if os.Getenv("SKIP_LINT_TESTS") == "1" {
		t.Skip("SKIP_LINT_TESTS is set")
	}
	root := golangciFixtureModule(t)

	analyzer := NewGolangciLintAnalyzer(root, "")
	findings, err := analyzer.Analyze(context.Background(), changedFile("internal/decode/decode.go"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if len(findings) == 0 {
		t.Fatal("expected at least 1 warning finding for unchecked json.Unmarshal")
	}
	var warningFound bool
	for _, f := range findings {
		if f.Tool != ToolGolangciLint {
			t.Errorf("Tool = %q, want %q", f.Tool, ToolGolangciLint)
		}
		if f.RuleID == "errcheck" && f.Severity == "warning" {
			warningFound = true
		}
		if f.Line < 1 {
			t.Errorf("Line = %d, want >= 1", f.Line)
		}
	}
	if !warningFound {
		t.Errorf("expected an errcheck warning, got %+v", findings)
	}
	// Fingerprint stability.
	for _, f := range findings {
		want := Fingerprint(ToolGolangciLint, f.RuleID, f.FilePath, f.Line)
		if f.ID != want {
			t.Errorf("ID = %q, want %q", f.ID, want)
		}
	}
}

func TestGolangciLintAnalyzerFiltersToRequestedFile(t *testing.T) {
	if os.Getenv("SKIP_LINT_TESTS") == "1" {
		t.Skip("SKIP_LINT_TESTS is set")
	}
	root := golangciFixtureModule(t)

	// Issues exist in internal/decode/decode.go; asking for another file in
	// the same module must return nothing.
	analyzer := NewGolangciLintAnalyzer(root, "")
	findings, err := analyzer.Analyze(context.Background(), changedFile("other/file.go"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %d, want 0 for unrelated file", len(findings))
	}
}

func TestGolangciLintAnalyzerBinaryNotInstalled(t *testing.T) {
	root := golangciFixtureModule(t)
	analyzer := NewGolangciLintAnalyzer(root, "")
	analyzer.Binary = "definitely-not-installed-linter-xyz"

	_, err := analyzer.Analyze(context.Background(), changedFile("internal/decode/decode.go"))
	if err == nil {
		t.Fatal("missing binary should produce an error")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %q, want explicit not-installed message", err)
	}
}

func TestGolangciLintAnalyzerDeletedFile(t *testing.T) {
	analyzer := NewGolangciLintAnalyzer(t.TempDir(), "")
	findings, err := analyzer.Analyze(context.Background(), diff.FileDiff{
		OldPath: "gone.go", ChangeType: diff.ChangeDeleted,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("deleted file should yield 0 findings, got %+v", findings)
	}
}
