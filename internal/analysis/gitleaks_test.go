package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codereviewagent/internal/diff"
)

// gitleaksWorkspace creates a temp workspace and writes the given files.
func gitleaksWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
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

// changedFile builds a changed-file entry for the analyzer.
func changedFile(path string) diff.FileDiff {
	return diff.FileDiff{Path: path, ChangeType: diff.ChangeModified}
}

func TestGitleaksAnalyzerDetectsCustomRuleSecret(t *testing.T) {
	root := gitleaksWorkspace(t, map[string]string{
		"internal/config/settings.go": "package config\n\nconst apiKey = \"sk-xxxxx\"\n",
	})
	analyzer, err := NewGitleaksAnalyzer(root, []SecretRule{
		{ID: "openai-style-key", Description: "OpenAI-style API key", Pattern: `sk-[A-Za-z0-9]+`, Keywords: []string{"sk-"}},
	})
	if err != nil {
		t.Fatalf("NewGitleaksAnalyzer: %v", err)
	}

	findings, err := analyzer.Analyze(context.Background(), changedFile("internal/config/settings.go"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least 1 critical finding for hardcoded sk- key")
	}
	f := findings[0]
	if f.Severity != "critical" {
		t.Errorf("Severity = %q, want critical", f.Severity)
	}
	if f.Tool != ToolGitleaks {
		t.Errorf("Tool = %q, want %q", f.Tool, ToolGitleaks)
	}
	if f.RuleID != "openai-style-key" {
		t.Errorf("RuleID = %q, want openai-style-key", f.RuleID)
	}
	if f.Line != 3 {
		t.Errorf("Line = %d, want 3", f.Line)
	}
	if f.ID != "gitleaks:openai-style-key:internal/config/settings.go:3" {
		t.Errorf("ID = %q, want stable fingerprint", f.ID)
	}
	if !containsAll(f.Snippet, []string{"sk-", "xxxxx"}) {
		t.Errorf("Snippet = %q, want the secret excerpt", f.Snippet)
	}
}

func TestGitleaksAnalyzerDefaultRules(t *testing.T) {
	// A realistic (non-canonical) AWS key matches the embedded default
	// rules; canonical example keys are excluded by gitleaks' allowlist.
	root := gitleaksWorkspace(t, map[string]string{
		"deploy/creds.go": "package deploy\n\nvar awsKey = \"AKIAQWERTYUIOPASDFGH\"\nvar awsSecret = \"AbCdEfGhIjKlMnOpQrStUvWxYz0123456789+/aa\"\n",
	})
	analyzer, err := NewGitleaksAnalyzer(root, nil)
	if err != nil {
		t.Fatalf("NewGitleaksAnalyzer: %v", err)
	}

	findings, err := analyzer.Analyze(context.Background(), changedFile("deploy/creds.go"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected default rules to catch AWS key")
	}
	for _, f := range findings {
		if f.Severity != "critical" {
			t.Errorf("Severity = %q, want critical", f.Severity)
		}
		if f.Line < 1 {
			t.Errorf("Line = %d, want 1-based", f.Line)
		}
	}
}

func TestGitleaksAnalyzerCleanFile(t *testing.T) {
	root := gitleaksWorkspace(t, map[string]string{
		"internal/util/math.go": "package util\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
	})
	analyzer, err := NewGitleaksAnalyzer(root, []SecretRule{
		{ID: "openai-style-key", Description: "OpenAI-style API key", Pattern: `sk-[A-Za-z0-9]+`},
	})
	if err != nil {
		t.Fatalf("NewGitleaksAnalyzer: %v", err)
	}

	findings, err := analyzer.Analyze(context.Background(), changedFile("internal/util/math.go"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("clean file should yield 0 findings, got %+v", findings)
	}
}

func TestGitleaksAnalyzerDeletedFileNoFindings(t *testing.T) {
	root := gitleaksWorkspace(t, nil)
	analyzer, err := NewGitleaksAnalyzer(root, nil)
	if err != nil {
		t.Fatalf("NewGitleaksAnalyzer: %v", err)
	}

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

func TestGitleaksAnalyzerMissingFileError(t *testing.T) {
	root := gitleaksWorkspace(t, nil)
	analyzer, err := NewGitleaksAnalyzer(root, nil)
	if err != nil {
		t.Fatalf("NewGitleaksAnalyzer: %v", err)
	}

	_, err = analyzer.Analyze(context.Background(), changedFile("no/such/file.go"))
	if err == nil {
		t.Error("missing file should produce an error, not panic")
	}
	if !containsAll(err.Error(), []string{"gitleaks"}) {
		t.Errorf("error %q should mention the tool name", err)
	}
}

func TestGitleaksAnalyzerInvalidCustomRule(t *testing.T) {
	root := gitleaksWorkspace(t, nil)
	if _, err := NewGitleaksAnalyzer(root, []SecretRule{
		{ID: "bad", Pattern: "([unclosed"},
	}); err == nil {
		t.Error("invalid custom rule regex should produce an error")
	}
}

func TestGitleaksAnalyzerPathEscape(t *testing.T) {
	root := gitleaksWorkspace(t, nil)
	analyzer, err := NewGitleaksAnalyzer(root, nil)
	if err != nil {
		t.Fatalf("NewGitleaksAnalyzer: %v", err)
	}

	if _, err := analyzer.Analyze(context.Background(), changedFile("../outside.go")); err == nil {
		t.Error("path traversal should be rejected")
	}
}

// containsAll reports whether s contains every needle.
func containsAll(s string, needles []string) bool {
	for _, n := range needles {
		if n != "" && !strings.Contains(s, n) {
			return false
		}
	}
	return true
}
