package vcs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// buildFixtureRepo creates a local repository with one commit; pullRef
// controls whether a refs/pull/1/head ref is registered (GitHub has it,
// bare fixtures may not).
func buildFixtureRepo(t *testing.T, pullRef bool) (dir, commitSHA string) {
	t.Helper()
	dir = t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	commit, err := w.Commit("c", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@e.c", When: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pullRef {
		if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/pull/1/head"), commit)); err != nil {
			t.Fatal(err)
		}
	}
	return dir, commit.String()
}

func TestPrepareWorkspaceWithPullRef(t *testing.T) {
	dir, sha := buildFixtureRepo(t, true)

	root, cleanup, err := PrepareWorkspace(context.Background(), "", dir, "o", "r", 1, sha)
	if err != nil {
		t.Fatalf("PrepareWorkspace: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(root, "a.go")); err != nil {
		t.Fatalf("checked out file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Log("note: .git may be a file or dir")
	}
}

func TestPrepareWorkspaceFallsBackToHeadSHA(t *testing.T) {
	dir, sha := buildFixtureRepo(t, false)

	root, cleanup, err := PrepareWorkspace(context.Background(), "", dir, "o", "r", 1, sha)
	if err != nil {
		t.Fatalf("PrepareWorkspace: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(root, "a.go")); err != nil {
		t.Fatalf("checked out file missing: %v", err)
	}
}

func TestPrepareWorkspaceCleanupRemovesDir(t *testing.T) {
	dir, sha := buildFixtureRepo(t, true)

	root, cleanup, err := PrepareWorkspace(context.Background(), "", dir, "o", "r", 1, sha)
	if err != nil {
		t.Fatalf("PrepareWorkspace: %v", err)
	}
	cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("workspace %s still exists after cleanup", root)
	}
}

func TestPrepareWorkspaceFailsWithoutRefOrSHA(t *testing.T) {
	dir, _ := buildFixtureRepo(t, false)

	_, _, err := PrepareWorkspace(context.Background(), "", dir, "o", "r", 1, "")
	if err == nil {
		t.Error("expected error without pull ref and head SHA")
	}
}
