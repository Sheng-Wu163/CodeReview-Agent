package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

func TestRunStaticAnalysisToolCommandNotInstalled(t *testing.T) {
	root := newWorkspace(t)
	tool := RunStaticAnalysisTool{
		Root:    root,
		Command: "definitely-not-installed-linter-xyz",
		Args:    []string{"run"},
	}

	out, err := tool.Call(context.Background(), "")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, notAvailableNotice) {
		t.Errorf("Call = %q, want fallback notice %q", out, notAvailableNotice)
	}
}

func TestRunStaticAnalysisToolSuccessfulRun(t *testing.T) {
	root := newWorkspace(t)

	var command string
	var args []string
	switch runtime.GOOS {
	case "windows":
		command, args = "cmd", []string{"/c", "echo", "lint output here"}
	default:
		command, args = "echo", []string{"lint output here"}
	}

	tool := RunStaticAnalysisTool{Root: root, Command: command, Args: args}
	out, err := tool.Call(context.Background(), "")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, "lint output here") {
		t.Errorf("Call = %q, want command output", out)
	}
}

func TestAllToolsImplementToolInterface(t *testing.T) {
	root := newWorkspace(t)
	all := []tools.Tool{
		ReadFileTool{Root: root},
		SearchCodeTool{Root: root},
		GetFunctionDefTool{Root: root},
		RunStaticAnalysisTool{Root: root},
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
