package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codereviewagent/internal/diff"
)

// semgrepFixtureJSON is a captured `semgrep scan --json` output (real run
// against the rules in rules/semgrep). Kept verbatim to lock the parser
// against format changes (spec 11.5).
const semgrepFixtureJSON = `{"errors":[],"results":[{"check_id":"codereviewagent.rules.semgrep.sql-injection-concat","path":"internal/db/users.go","start":{"line":11,"col":16},"end":{"line":11,"col":64},"extra":{"fingerprint":"x","lines":"\trows, err := db.Query(\"SELECT * FROM users WHERE id = \" + userID)","message":"SQL built by string concatenation or Sprintf with user input allows SQL injection","metadata":{},"severity":"ERROR"}}],"paths":{"scanned":["internal/db/users.go"]}}`

func TestSemgrepJSONParser(t *testing.T) {
	parsed, err := parseSemgrepJSON([]byte(semgrepFixtureJSON))
	if err != nil {
		t.Fatalf("parseSemgrepJSON: %v", err)
	}
	if len(parsed.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(parsed.Results))
	}
	r := parsed.Results[0]
	if r.CheckID != "codereviewagent.rules.semgrep.sql-injection-concat" {
		t.Errorf("check_id = %q", r.CheckID)
	}
	if r.Start.Line != 11 || r.End.Line != 11 {
		t.Errorf("lines = %d-%d, want 11-11", r.Start.Line, r.End.Line)
	}
	if r.Extra.Severity != "ERROR" {
		t.Errorf("severity = %q, want ERROR", r.Extra.Severity)
	}
}

func TestSemgrepJSONParserInvalid(t *testing.T) {
	if _, err := parseSemgrepJSON([]byte("not-json{")); err == nil {
		t.Error("invalid JSON should produce an error")
	}
}

func TestSemgrepNormalizeRuleID(t *testing.T) {
	tests := []struct {
		checkID  string
		rulesDir string
		want     string
	}{
		{
			// Absolute dir on this machine, CWD somewhere else.
			checkID:  "codereviewagent.rules.semgrep.sql-injection-concat",
			rulesDir: "C:\\proj\\codereviewagent\\rules\\semgrep",
			want:     "sql-injection-concat",
		},
		{
			checkID:  "custom.rules.semgrep.command-injection-concat",
			rulesDir: "/opt/app/rules/semgrep",
			want:     "command-injection-concat",
		},
		{
			// Unprefixed id passes through unchanged.
			checkID:  "sql-injection-concat",
			rulesDir: "rules/semgrep",
			want:     "sql-injection-concat",
		},
		{
			// Unknown prefix (e.g. third-party registry rules) is kept.
			checkID:  "registry.go.sql-injection",
			rulesDir: "rules/semgrep",
			want:     "registry.go.sql-injection",
		},
	}
	for _, tt := range tests {
		if got := normalizeRuleID(tt.checkID, tt.rulesDir); got != tt.want {
			t.Errorf("normalizeRuleID(%q, %q) = %q, want %q", tt.checkID, tt.rulesDir, got, tt.want)
		}
	}
}

func TestSemgrepSeverityMapping(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ERROR", "critical"},
		{"WARNING", "warning"},
		{"INFO", "suggestion"},
		{"INVENTORY", "suggestion"},
		{"", "suggestion"},
	}
	for _, tt := range tests {
		if got := severityFromSemgrep(tt.in); got != tt.want {
			t.Errorf("severityFromSemgrep(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// semgrepFixtureWorkspace creates a workspace with the vulnerable file.
func semgrepFixtureWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	full := filepath.Join(root, "internal", "db", "users.go")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "package db\n\nimport \"database/sql\"\n\nfunc GetUser(db *sql.DB, userID string) {\n" +
		"\trows, err := db.Query(\"SELECT * FROM users WHERE id = \" + userID)\n" +
		"\tif err != nil {\n\t\tpanic(err)\n\t}\n\tdefer rows.Close()\n}\n"
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSemgrepAnalyzerDetectsSQLInjection(t *testing.T) {
	if os.Getenv("SKIP_SEMGREP_TESTS") == "1" {
		t.Skip("SKIP_SEMGREP_TESTS is set")
	}
	root := semgrepFixtureWorkspace(t)
	rulesDir := copyRulesInto(t, root)
	analyzer := NewSemgrepAnalyzer(root, rulesDir)

	findings, err := analyzer.Analyze(context.Background(), changedFile("internal/db/users.go"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least 1 critical SQL injection finding")
	}
	f := findings[0]
	if f.Severity != "critical" {
		t.Errorf("Severity = %q, want critical", f.Severity)
	}
	if f.RuleID != "sql-injection-concat" {
		t.Errorf("RuleID = %q, want sql-injection-concat (prefix stripped)", f.RuleID)
	}
	if f.Line != 6 {
		t.Errorf("Line = %d, want 7", f.Line)
	}
	if f.ID != "semgrep:sql-injection-concat:internal/db/users.go:6" {
		t.Errorf("ID = %q, want stable fingerprint", f.ID)
	}
	if !strings.Contains(f.Snippet, "db.Query") {
		t.Errorf("Snippet = %q, want matched source line", f.Snippet)
	}
}

func TestSemgrepAnalyzerBinaryNotInstalled(t *testing.T) {
	root := semgrepFixtureWorkspace(t)
	analyzer := NewSemgrepAnalyzer(root, copyRulesInto(t, root))
	analyzer.Binary = "definitely-not-installed-semgrep-xyz"

	_, err := analyzer.Analyze(context.Background(), changedFile("internal/db/users.go"))
	if err == nil {
		t.Fatal("missing binary should produce an error")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %q, want explicit not-installed message", err)
	}
}

func TestSemgrepAnalyzerNoRulesConfigured(t *testing.T) {
	analyzer := NewSemgrepAnalyzer(t.TempDir(), "")
	if _, err := analyzer.Analyze(context.Background(), changedFile("a.go")); err == nil {
		t.Error("missing rules dir should produce an error")
	}
}

func TestSemgrepAnalyzerDeletedFile(t *testing.T) {
	analyzer := NewSemgrepAnalyzer(t.TempDir(), "rules/semgrep")
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

// copyRulesInto copies the repository rule files into the given workspace
// under rules/semgrep and returns the rules dir path to use.
func copyRulesInto(t *testing.T, root string) string {
	t.Helper()
	srcDir := "../../rules/semgrep"
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Skipf("rules dir unavailable: %v", err)
	}
	target := filepath.Join(root, "rules", "semgrep")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".yml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return "rules/semgrep"
}
