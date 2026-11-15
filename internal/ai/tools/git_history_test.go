package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// newGitWorkspace creates a temporary repository with one committed file.
func newGitWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("main.go"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Alice", Email: "alice@example.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return root
}

func TestGitBlameTool(t *testing.T) {
	root := newGitWorkspace(t)
	tool := GitBlameTool{Root: root}

	out, err := tool.Call(context.Background(), "main.go")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	for _, want := range []string{"Alice", "package main", "func main()"} {
		if !strings.Contains(out, want) {
			t.Errorf("blame output missing %q:\n%s", want, out)
		}
	}

	out, err = tool.Call(context.Background(), "main.go:1:1")
	if err != nil {
		t.Fatalf("range Call: %v", err)
	}
	if strings.Contains(out, "func main()") {
		t.Errorf("range 1:1 must not include line 3:\n%s", out)
	}
}

func TestGitBlameUntrackedFile(t *testing.T) {
	root := newGitWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "untracked.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := (GitBlameTool{Root: root}).Call(context.Background(), "untracked.go")
	if err == nil {
		t.Fatalf("Call = %q, want clear error without panic (AC-147)", out)
	}
	if !strings.Contains(err.Error(), "not tracked") {
		t.Errorf("error = %v, want a clear untracked-file error", err)
	}
}

func TestGitToolsRejectOptionLikePaths(t *testing.T) {
	root := newGitWorkspace(t)
	tests := []struct {
		name string
		call func() (string, error)
	}{
		{
			name: "blame long option",
			call: func() (string, error) {
				return (GitBlameTool{Root: root}).Call(context.Background(), "--output=/tmp/x")
			},
		},
		{
			name: "blame short option with range",
			call: func() (string, error) {
				return (GitBlameTool{Root: root}).Call(context.Background(), "-n:1:5")
			},
		},
		{
			name: "log long option",
			call: func() (string, error) {
				return (GitLogFileTool{Root: root}).Call(context.Background(), "--output=/tmp/x:5")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.call(); err == nil {
				t.Fatal("Call error = nil, want rejection (AC-148)")
			}
		})
	}
}

func TestGitLogFileTool(t *testing.T) {
	root := newGitWorkspace(t)
	tool := GitLogFileTool{Root: root}

	out, err := tool.Call(context.Background(), "main.go")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, "initial commit") || !strings.Contains(out, "Alice") {
		t.Errorf("log output missing commit metadata:\n%s", out)
	}
}

func TestParseGitLogLimitCapped(t *testing.T) {
	path, limit, err := parseGitLogInput("a.go:100")
	if err != nil {
		t.Fatalf("parseGitLogInput: %v", err)
	}
	if path != "a.go" || limit != maxLogCommits {
		t.Errorf("parseGitLogInput = %q,%d; want a.go,%d", path, limit, maxLogCommits)
	}
}
