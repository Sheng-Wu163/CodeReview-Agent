package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tmc/langchaingo/tools"
)

// newWorkspace creates a temporary review workspace with a few files and
// returns its absolute root.
func newWorkspace(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, "internal", "util"), 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("main.go", `package main

import "fmt"

// Handle processes an event.
func Handle(name string) error {
	if name == "" {
		return fmt.Errorf("empty name")
	}
	fmt.Println(name)
	return nil
}
`)
	write("internal/util/util.go", `package util

func Double(n int) int {
	return n * 2
}
`)
	write("README.md", "# Test workspace\n")
	return root
}

func TestReadFileTool(t *testing.T) {
	root := newWorkspace(t)
	tool := ReadFileTool{Root: root}

	tests := []struct {
		name    string
		input   string
		wantSub string
		wantErr bool
	}{
		{
			name:    "whole file",
			input:   "main.go",
			wantSub: "func Handle(name string) error {",
		},
		{
			name:    "line range",
			input:   "main.go:6:7",
			wantSub: "6\tfunc Handle(name string) error {",
		},
		{
			name:    "nested path",
			input:   "internal/util/util.go",
			wantSub: "func Double(n int) int {",
		},
		{
			name:    "missing file",
			input:   "nope.go",
			wantErr: true,
		},
		{
			name:    "path traversal",
			input:   "../outside/secret.txt",
			wantErr: true,
		},
		{
			name:    "absolute path",
			input:   filepath.ToSlash(filepath.Join(root, "main.go")),
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid range",
			input:   "main.go:10:5",
			wantErr: true,
		},
		{
			name:    "non numeric line",
			input:   "main.go:a:b",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tool.Call(context.Background(), tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Call(%q) = %q, want error", tt.input, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("Call(%q): %v", tt.input, err)
			}
			if !strings.Contains(out, tt.wantSub) {
				t.Errorf("Call(%q) = %q, want substring %q", tt.input, out, tt.wantSub)
			}
		})
	}
}

func TestReadFileToolPathTraversalBlocked(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("s3cret"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := ReadFileTool{Root: root}
	if _, err := tool.Call(context.Background(), "../secret.txt"); err == nil {
		t.Error("path traversal should be rejected")
	}
}

func TestSearchCodeTool(t *testing.T) {
	root := newWorkspace(t)
	tool := SearchCodeTool{Root: root}

	tests := []struct {
		name    string
		input   string
		wantSub string
		notWant string
		wantErr bool
	}{
		{
			name:    "search all files",
			input:   `func\s+\w+\(n int\)`,
			wantSub: "internal/util/util.go:3:",
		},
		{
			name:    "glob restricted",
			input:   `package main | main.go`,
			wantSub: "main.go:1: package main",
		},
		{
			name:    "glob excludes README",
			input:   `Test workspace | *.md`,
			notWant: "no matches found",
		},
		{
			name:    "no matches",
			input:   `this-does-not-exist-xyz`,
			wantSub: "no matches found",
		},
		{
			name:    "invalid regex",
			input:   `([unclosed`,
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tool.Call(context.Background(), tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Call(%q) = %q, want error", tt.input, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("Call(%q): %v", tt.input, err)
			}
			if tt.wantSub != "" && !strings.Contains(out, tt.wantSub) {
				t.Errorf("Call(%q) = %q, want substring %q", tt.input, out, tt.wantSub)
			}
			if tt.notWant != "" && strings.Contains(out, tt.notWant) {
				t.Errorf("Call(%q) = %q, must not contain %q", tt.input, out, tt.notWant)
			}
		})
	}
}

func TestSearchCodeToolSkipsVendorAndHidden(t *testing.T) {
	root := newWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "vendor", "example.com"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "vendor", "example.com", "vendored.go"),
		[]byte("package vendored\n\nfunc UniqueNeedle() {}\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	tool := SearchCodeTool{Root: root}
	out, err := tool.Call(context.Background(), "UniqueNeedle")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out != "no matches found" {
		t.Errorf("vendor code must be skipped, got %q", out)
	}
}

func TestGetFunctionDefTool(t *testing.T) {
	root := newWorkspace(t)
	tool := GetFunctionDefTool{Root: root}

	tests := []struct {
		name    string
		input   string
		wantSub []string
		wantErr bool
	}{
		{
			name:    "top level function",
			input:   "Handle",
			wantSub: []string{"main.go:", "func Handle(name string) error {", `fmt.Errorf("empty name")`, "return nil"},
		},
		{
			name:    "nested package function",
			input:   "Double",
			wantSub: []string{"internal/util/util.go:", "func Double(n int) int {", "return n * 2"},
		},
		{
			name:    "not found",
			input:   "DoesNotExist",
			wantErr: true,
		},
		{
			name:    "empty name",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tool.Call(context.Background(), tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Call(%q) = %q, want error", tt.input, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("Call(%q): %v", tt.input, err)
			}
			for _, sub := range tt.wantSub {
				if !strings.Contains(out, sub) {
					t.Errorf("Call(%q) = %q, want substring %q", tt.input, out, sub)
				}
			}
		})
	}
}

func TestAllToolsImplementToolInterface(t *testing.T) {
	root := newWorkspace(t)
	all := []tools.Tool{
		ReadFileTool{Root: root},
		SearchCodeTool{Root: root},
		GetFunctionDefTool{Root: root},
	}
	for _, tool := range all {
		if tool.Name() == "" {
			t.Errorf("%T has empty Name()", tool)
		}
		if tool.Description() == "" {
			t.Errorf("%T has empty Description()", tool)
		}
	}
}

func TestReadFileToolTruncatesLargeOutput(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "%04d\tconst value%d = 1234567890\n", i+1, i)
	}
	if err := os.WriteFile(filepath.Join(root, "large.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := ReadFileTool{Root: root, MaxOutputChars: 1000}
	out, err := tool.Call(context.Background(), "large.go")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, TruncationWarning) {
		t.Errorf("output must contain %q", TruncationWarning)
	}
	if !strings.Contains(out, "0001") {
		t.Errorf("output must keep the beginning of the file")
	}
	if strings.Contains(out, "3000") {
		t.Errorf("output must not contain the end of a 3000-line file")
	}
	if got := utf8.RuneCountInString(out); got > 1000 {
		t.Errorf("output length = %d runes, want at most the 1000 limit", got)
	}
}

func TestSearchCodeToolTruncatesMatchCount(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "needle line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "needle.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := SearchCodeTool{Root: root}
	out, err := tool.Call(context.Background(), "needle | *.txt")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, TruncationWarning) {
		t.Errorf("output must contain %q", TruncationWarning)
	}
	if got := strings.Count(out, "needle.txt:"); got != MaxSearchResults {
		t.Errorf("returned matches = %d, want %d", got, MaxSearchResults)
	}
	if !strings.Contains(out, "匹配过多") {
		t.Errorf("output should suggest a more precise query: %q", out)
	}
}
