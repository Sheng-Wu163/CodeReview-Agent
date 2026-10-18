package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
	itools "codereviewagent/internal/ai/tools"
	"codereviewagent/internal/repository"
)

// mockGitHub implements GitHubAPI, recording all calls.
type mockGitHub struct {
	mu       sync.Mutex
	diff     string
	diffErr  error
	reviews  []map[string]any
	statuses [][2]string // state, description
}

func (m *mockGitHub) GetPRDiff(_ context.Context, _, _ string, _ int) (string, error) {
	return m.diff, m.diffErr
}

func (m *mockGitHub) SubmitFindingsReview(_ context.Context, _, _ string, _ int, _ string, findings []agents.VerifiedFinding) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reviews = append(m.reviews, map[string]any{
		"kind": "findings", "count": len(findings), "findings": findings,
	})
	return "COMMENT", nil
}

func (m *mockGitHub) SubmitLGTMReview(_ context.Context, _, _ string, _ int, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reviews = append(m.reviews, map[string]any{"kind": "lgtm"})
	return nil
}

func (m *mockGitHub) SubmitNoMeaningfulChangesReview(_ context.Context, _, _ string, _ int, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reviews = append(m.reviews, map[string]any{"kind": "no-changes"})
	return nil
}

func (m *mockGitHub) UpdateCommitStatus(_ context.Context, _, _, _, state, description string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses = append(m.statuses, [2]string{state, description})
	return nil
}

func (m *mockGitHub) lastReviewKind() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.reviews) == 0 {
		return ""
	}
	return m.reviews[len(m.reviews)-1]["kind"].(string)
}

// mockStore implements ReviewStore, recording calls.
type mockStore struct {
	mu       sync.Mutex
	nextID   int64
	reviews  map[int64]*repository.Review
	findings []repository.Finding
}

func newMockStore() *mockStore {
	return &mockStore{nextID: 1, reviews: make(map[int64]*repository.Review)}
}

func (m *mockStore) CreateReview(_ context.Context, rev *repository.Review) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rev.ID = m.nextID
	m.reviews[rev.ID] = rev
	m.nextID++
	return rev.ID, nil
}

func (m *mockStore) UpdateReviewStatus(_ context.Context, id int64, status, summary *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := status; s != nil {
		m.reviews[id].Status = *s
	}
	if s := summary; s != nil {
		m.reviews[id].Summary = *s
	}
	return nil
}

func (m *mockStore) UpdateReviewPreFindings(_ context.Context, id int64, preFindings string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rev := m.reviews[id]
	if rev == nil {
		return fmt.Errorf("review %d not found", id)
	}
	rev.PreFindings = preFindings
	return nil
}

func (m *mockStore) UpdateReviewAgentStatus(_ context.Context, id int64, status map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rev := m.reviews[id]
	if rev == nil {
		return fmt.Errorf("review %d not found", id)
	}
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	rev.AgentStatus = string(data)
	return nil
}

func (m *mockStore) CreateFinding(_ context.Context, f *repository.Finding) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findings = append(m.findings, *f)
	return int64(len(m.findings)), nil
}

// buildLocalRepo creates a local git repository with one commit and returns
// its path plus the commit hash.
func buildLocalRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "users.go"), []byte("package main\n\nrows, err := db.Query(\"SELECT * FROM users WHERE id = \" + userID)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	commit, err := w.Commit("test commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Register the PR head ref so PrepareWorkspace takes the main path.
	if err := repo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.ReferenceName("refs/pull/1/head"), commit)); err != nil {
		t.Fatal(err)
	}
	return dir, commit.String()
}

// testDiff is a diff touching users.go (the file in the local repo).
const testDiff = `diff --git a/users.go b/users.go
index 83db48f..2c47bf9 100644
--- a/users.go
+++ b/users.go
@@ -1,3 +1,3 @@
 package main
-rows, err := db.Query("SELECT * FROM users")
+rows, err := db.Query("SELECT * FROM users WHERE id = " + userID)
`

func newTestPipeline(t *testing.T, gh *mockGitHub, store *mockStore, findings *[]agents.AgentFinding) *Pipeline {
	t.Helper()

	// One scripted agent reporting the injected findings.
	agent := &mockAgent{name: "security", findings: *findings}

	// Orchestrator with a real verifier bound to each run's workspace root
	// (which is the cloned local repo in tests).
	o := New(zap.NewNop(), "", []agents.ReviewAgent{agent},
		func(root string) Verifier { return agents.NewVerifier(itools.ReadFileTool{Root: root}, nil) }, nil)

	p := NewPipeline(o, gh, store, "fake-token", zap.NewNop())
	return p
}

func TestPipelineEndToEndWithLocalRepo(t *testing.T) {
	repoDir, commitSHA := buildLocalRepo(t)

	gh := &mockGitHub{diff: testDiff}
	store := newMockStore()
	findings := []agents.AgentFinding{{
		Agent: "security", Severity: agents.SeverityCritical,
		FilePath: "users.go", LineNumber: 2,
		Message:     "SQL injection",
		CodeSnippet: "db.Query(\"SELECT * FROM users WHERE id = \" + userID)",
		Suggestion:  "parameterize", Confidence: 0.9,
	}}
	p := newTestPipeline(t, gh, store, &findings)
	p.SetCloneURL(repoDir)

	p.Process(context.Background(), EventInput{RepoFullName: "o/r", PRNumber: 1, HeadSHA: commitSHA})

	// Review persisted: pending -> completed.
	store.mu.Lock()
	rev := store.reviews[1]
	nFindings := len(store.findings)
	store.mu.Unlock()
	if rev == nil {
		t.Fatal("review record not created")
	}
	if rev.Status != "completed" {
		t.Errorf("status = %q, want completed", rev.Status)
	}
	if nFindings != 1 {
		t.Errorf("persisted findings = %d, want 1", nFindings)
	}

	// GitHub review submitted with findings + status failure (critical).
	if got := gh.lastReviewKind(); got != "findings" {
		t.Errorf("review kind = %q, want findings", got)
	}
	gh.mu.Lock()
	st := gh.statuses
	gh.mu.Unlock()
	if len(st) != 1 || st[0][0] != "failure" {
		t.Errorf("statuses = %v, want [failure ...] (AC-11)", st)
	}
}

func TestPipelineLGTMWhenNoFindings(t *testing.T) {
	repoDir, commitSHA := buildLocalRepo(t)

	gh := &mockGitHub{diff: testDiff}
	store := newMockStore()
	empty := []agents.AgentFinding{}
	p := newTestPipeline(t, gh, store, &empty)
	p.SetCloneURL(repoDir)

	p.Process(context.Background(), EventInput{RepoFullName: "o/r", PRNumber: 1, HeadSHA: commitSHA})

	if got := gh.lastReviewKind(); got != "lgtm" {
		t.Errorf("review kind = %q, want lgtm (AC-17)", got)
	}
	gh.mu.Lock()
	st := gh.statuses
	gh.mu.Unlock()
	if len(st) != 1 || st[0][0] != "success" {
		t.Errorf("statuses = %v, want success", st)
	}
}

func TestPipelineDiffErrorFailsReview(t *testing.T) {
	repoDir, _ := buildLocalRepo(t)

	gh := &mockGitHub{diffErr: context.DeadlineExceeded}
	store := newMockStore()
	empty := []agents.AgentFinding{}
	p := newTestPipeline(t, gh, store, &empty)
	p.SetCloneURL(repoDir)

	p.Process(context.Background(), EventInput{RepoFullName: "o/r", PRNumber: 1, HeadSHA: "sha"})

	store.mu.Lock()
	rev := store.reviews[1]
	store.mu.Unlock()
	if rev == nil || rev.Status != "failed" {
		t.Errorf("review = %+v, want failed status", rev)
	}
	gh.mu.Lock()
	st := gh.statuses
	gh.mu.Unlock()
	if len(st) != 1 || st[0][0] != "failure" {
		t.Errorf("statuses = %v, want failure", st)
	}
}

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		in    string
		owner string
		name  string
		ok    bool
	}{
		{"owner/repo", "owner", "repo", true},
		{"a/b/c", "a", "b/c", true},
		{"nonsense", "", "", false},
		{"", "", "", false},
		{"a/", "", "", false},
	}
	for _, tt := range tests {
		owner, name, ok := splitRepo(tt.in)
		if ok != tt.ok || owner != tt.owner || name != tt.name {
			t.Errorf("splitRepo(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.in, owner, name, ok, tt.owner, tt.name, tt.ok)
		}
	}
}
