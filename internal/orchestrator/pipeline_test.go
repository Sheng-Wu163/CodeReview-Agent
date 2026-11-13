package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
	itools "codereviewagent/internal/ai/tools"
	"codereviewagent/internal/github"
	"codereviewagent/internal/repository"
	"codereviewagent/internal/review"
)

// mockGitHub implements GitHubAPI, recording all calls.
type mockGitHub struct {
	mu          sync.Mutex
	diff        string
	diffErr     error
	compareDiff string
	compareErr  error
	threads     []review.ThreadState
	threadsErr  error
	resolved    []string
	reviews     []map[string]any
	statuses    [][2]string // state, description
}

func (m *mockGitHub) GetCompareDiff(_ context.Context, _, _, _, _ string) (string, error) {
	return m.compareDiff, m.compareErr
}

func (m *mockGitHub) ListReviewThreads(_ context.Context, _, _ string, _ int) ([]review.ThreadState, error) {
	return m.threads, m.threadsErr
}

func (m *mockGitHub) ResolveReviewThread(_ context.Context, threadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolved = append(m.resolved, threadID)
	return nil
}

func (m *mockGitHub) SubmitReviewPlan(_ context.Context, _, _ string, _ int, _ string, plan review.PublishPlan, warnings []string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reviews = append(m.reviews, map[string]any{
		"kind": "findings", "count": len(plan.New), "findings": plan.New,
		"persisting": plan.Persisting, "resolved": plan.Resolved,
		"event": plan.Event, "warnings": warnings,
	})
	return plan.Event, nil
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

func (m *mockGitHub) SubmitFindingsReviewWithWarnings(_ context.Context, _, _ string, _ int, _ string, findings []agents.VerifiedFinding, warnings []string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reviews = append(m.reviews, map[string]any{
		"kind": "findings", "count": len(findings), "findings": findings, "warnings": warnings,
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
	// latestCompleted is returned by LatestCompletedReview when set.
	latestCompleted *repository.Review
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

func (m *mockStore) UpdateReviewAgentResults(_ context.Context, id int64, results string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rev := m.reviews[id]
	if rev == nil {
		return fmt.Errorf("review %d not found", id)
	}
	rev.AgentResults = results
	return nil
}

func (m *mockStore) UpdateReviewAnchor(_ context.Context, id int64, reviewedSHA, baseSHA, scope, scopeReason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rev := m.reviews[id]
	if rev == nil {
		return fmt.Errorf("review %d not found", id)
	}
	rev.ReviewedSHA = reviewedSHA
	rev.BaseSHA = baseSHA
	rev.Scope = scope
	rev.ScopeReason = scopeReason
	return nil
}

func (m *mockStore) LatestCompletedReview(_ context.Context, _ string, _ int) (*repository.Review, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latestCompleted == nil {
		return nil, fmt.Errorf("no completed review")
	}
	copied := *m.latestCompleted
	return &copied, nil
}

func (m *mockStore) ListFindings(_ context.Context, reviewID int64) ([]repository.Finding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []repository.Finding
	for _, f := range m.findings {
		if f.ReviewID == reviewID {
			out = append(out, f)
		}
	}
	return out, nil
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

	// One scripted agent reporting the injected findings. It performs one
	// read_file call so the Harness completion gate accepts it as completed.
	agent := &mockAgent{name: "security", findings: *findings, useTools: true, toolInput: "users.go"}

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

func TestPipelineRecordsFailedAgentAndWarns(t *testing.T) {
	repoDir, commitSHA := buildLocalRepo(t)

	gh := &mockGitHub{diff: testDiff}
	store := newMockStore()
	failing := &mockAgent{name: "security", err: fmt.Errorf("llm timeout")}
	healthy := &mockAgent{
		name:      "logic",
		findings:  []agents.AgentFinding{finding("logic", agents.SeverityWarning, "unhandled error")},
		useTools:  true,
		toolInput: "users.go",
	}
	o := New(zap.NewNop(), "", []agents.ReviewAgent{failing, healthy},
		func(root string) Verifier { return agents.NewVerifier(itools.ReadFileTool{Root: root}, nil) }, nil)
	o.SetReviewStrategy(fixedAssessor{level: review.ComplexityStandard}, review.NewSelector())

	p := NewPipeline(o, gh, store, "fake-token", zap.NewNop())
	p.SetCloneURL(repoDir)
	p.Process(context.Background(), EventInput{RepoFullName: "o/r", PRNumber: 1, HeadSHA: commitSHA})

	// The failed agent is persisted with its structured result (T52).
	store.mu.Lock()
	rev := store.reviews[1]
	store.mu.Unlock()
	if rev == nil {
		t.Fatal("review record not created")
	}
	var results map[string]map[string]any
	if err := json.Unmarshal([]byte(rev.AgentResults), &results); err != nil {
		t.Fatalf("unmarshal AgentResults %q: %v", rev.AgentResults, err)
	}
	if results["security"]["status"] != "failed" {
		t.Errorf("security status = %v, want failed", results["security"]["status"])
	}

	// The review body warns that the security dimension did not complete
	// (AC-106), and the pipeline still completes.
	gh.mu.Lock()
	last := gh.reviews[len(gh.reviews)-1]
	gh.mu.Unlock()
	if last["kind"] != "findings" {
		t.Errorf("review kind = %v, want findings with warnings", last["kind"])
	}
	warnings, _ := last["warnings"].([]string)
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "安全审查未完成") {
		t.Errorf("warnings = %q, want 安全审查未完成", joined)
	}
	if rev.Status != "completed" {
		t.Errorf("review status = %q, want completed", rev.Status)
	}
}

// AC-117: a reachable anchor uses the Compare API and records incremental
// scope.
func TestPipelineIncrementalUsesCompare(t *testing.T) {
	repoDir, commitSHA := buildLocalRepo(t)
	gh := &mockGitHub{diff: "this is not a diff", compareDiff: testDiff}
	store := newMockStore()
	store.latestCompleted = &repository.Review{
		ID: 99, RepoFullName: "o/r", PRNumber: 1, HeadSHA: "old-head",
		Status: "completed", ReviewedSHA: "anchor-sha",
	}
	empty := []agents.AgentFinding{}
	p := newTestPipeline(t, gh, store, &empty)
	p.SetCloneURL(repoDir)

	p.Process(context.Background(), EventInput{
		RepoFullName: "o/r", PRNumber: 1, HeadSHA: commitSHA, BeforeSHA: "before-sha",
	})

	store.mu.Lock()
	rev := store.reviews[1]
	store.mu.Unlock()
	if rev == nil {
		t.Fatal("review record not created")
	}
	if rev.Scope != "incremental" {
		t.Errorf("Scope = %q, want incremental", rev.Scope)
	}
	if rev.BaseSHA != "anchor-sha" {
		t.Errorf("BaseSHA = %q, want anchor-sha (anchor takes priority over before)", rev.BaseSHA)
	}
	if rev.ScopeReason != "" {
		t.Errorf("ScopeReason = %q, want empty", rev.ScopeReason)
	}
	if rev.Status != "completed" {
		t.Errorf("Status = %q, want completed (full fallback would fail to parse)", rev.Status)
	}
}

// AC-118: an unreachable base falls back to the full diff.
func TestPipelineFallsBackWhenBaseUnreachable(t *testing.T) {
	repoDir, commitSHA := buildLocalRepo(t)
	gh := &mockGitHub{diff: testDiff, compareErr: github.ErrCompareBaseUnreachable}
	store := newMockStore()
	empty := []agents.AgentFinding{}
	p := newTestPipeline(t, gh, store, &empty)
	p.SetCloneURL(repoDir)

	p.Process(context.Background(), EventInput{
		RepoFullName: "o/r", PRNumber: 1, HeadSHA: commitSHA, BeforeSHA: "gone-sha",
	})

	store.mu.Lock()
	rev := store.reviews[1]
	store.mu.Unlock()
	if rev.Scope != "full" {
		t.Errorf("Scope = %q, want full", rev.Scope)
	}
	if rev.ScopeReason != "before_unreachable" {
		t.Errorf("ScopeReason = %q, want before_unreachable", rev.ScopeReason)
	}
}

// AC-126: an existing thread suppresses the duplicate inline comment.
func TestPipelineDeduplicatesExistingThreads(t *testing.T) {
	repoDir, commitSHA := buildLocalRepo(t)
	gh := &mockGitHub{diff: testDiff}
	finding := agents.AgentFinding{
		Agent: "security", Severity: agents.SeverityCritical,
		FilePath: "users.go", LineNumber: 2, Message: "SQL injection",
		CodeSnippet: `db.Query("SELECT * FROM users WHERE id = " + userID)`,
		Confidence:  0.9,
	}
	gh.threads = []review.ThreadState{{
		ThreadID: "t1", Fingerprint: review.FindingFingerprint(finding.FilePath, finding.Agent, finding.Message),
		Path: "users.go", Line: 2, IsResolved: false,
	}}
	store := newMockStore()
	findings := []agents.AgentFinding{finding}
	p := newTestPipeline(t, gh, store, &findings)
	p.SetCloneURL(repoDir)

	p.Process(context.Background(), EventInput{RepoFullName: "o/r", PRNumber: 1, HeadSHA: commitSHA})

	gh.mu.Lock()
	last := gh.reviews[len(gh.reviews)-1]
	gh.mu.Unlock()
	if last["count"].(int) != 0 {
		t.Errorf("new inline comments = %v, want 0", last["count"])
	}
	if persisting, _ := last["persisting"].([]agents.VerifiedFinding); len(persisting) != 1 {
		t.Errorf("persisting = %v, want 1", last["persisting"])
	}
}

// AC-127: only the genuinely new finding is submitted.
func TestPipelineSubmitsOnlyNewFinding(t *testing.T) {
	repoDir, commitSHA := buildLocalRepo(t)
	existing := agents.AgentFinding{
		Agent: "logic", Severity: agents.SeverityWarning,
		FilePath: "users.go", LineNumber: 2, Message: "existing issue", Confidence: 0.8,
	}
	fresh := agents.AgentFinding{
		Agent: "security", Severity: agents.SeverityCritical,
		FilePath: "users.go", LineNumber: 2, Message: "brand new issue",
		CodeSnippet: `db.Query("SELECT * FROM users WHERE id = " + userID)`, Confidence: 0.9,
	}
	gh := &mockGitHub{diff: testDiff, threads: []review.ThreadState{{
		ThreadID: "t1", Fingerprint: review.FindingFingerprint(existing.FilePath, existing.Agent, existing.Message),
		Path: "users.go", Line: 2,
	}}}
	store := newMockStore()
	findings := []agents.AgentFinding{existing, fresh}
	p := newTestPipeline(t, gh, store, &findings)
	p.SetCloneURL(repoDir)

	p.Process(context.Background(), EventInput{RepoFullName: "o/r", PRNumber: 1, HeadSHA: commitSHA})

	gh.mu.Lock()
	last := gh.reviews[len(gh.reviews)-1]
	gh.mu.Unlock()
	if last["count"].(int) != 1 {
		t.Errorf("new inline comments = %v, want 1", last["count"])
	}
	submitted := last["findings"].([]agents.VerifiedFinding)
	if submitted[0].Message != "brand new issue" {
		t.Errorf("submitted = %+v, want only the new finding", submitted)
	}
}

// AC-131: a thread-read failure degrades to publishing every finding.
func TestPipelineThreadReadFailureFallsBack(t *testing.T) {
	repoDir, commitSHA := buildLocalRepo(t)
	finding := agents.AgentFinding{
		Agent: "security", Severity: agents.SeverityCritical,
		FilePath: "users.go", LineNumber: 2, Message: "SQL injection",
		CodeSnippet: `db.Query("SELECT * FROM users WHERE id = " + userID)`, Confidence: 0.9,
	}
	gh := &mockGitHub{diff: testDiff, threadsErr: errors.New("rate limited")}
	store := newMockStore()
	findings := []agents.AgentFinding{finding}
	p := newTestPipeline(t, gh, store, &findings)
	p.SetCloneURL(repoDir)

	p.Process(context.Background(), EventInput{RepoFullName: "o/r", PRNumber: 1, HeadSHA: commitSHA})

	gh.mu.Lock()
	last := gh.reviews[len(gh.reviews)-1]
	gh.mu.Unlock()
	if last["count"].(int) != 1 {
		t.Errorf("new inline comments = %v, want 1 (no dedup fallback)", last["count"])
	}
	warnings, _ := last["warnings"].([]string)
	if !strings.Contains(strings.Join(warnings, "\n"), "线程读取失败") {
		t.Errorf("warnings = %v, want thread-read warning", warnings)
	}
}

// AC-128: a fixed PRIOR_FINDING resolves its open thread.
func TestPipelineResolvesFixedThread(t *testing.T) {
	repoDir, commitSHA := buildLocalRepo(t)
	gh := &mockGitHub{diff: testDiff, threads: []review.ThreadState{{
		ThreadID: "t-fixed", Fingerprint: "fp-fixed", Path: "users.go", Line: 2, IsResolved: false,
	}}}
	store := newMockStore()
	verdictFinding := agents.AgentFinding{
		Agent: "security", Severity: agents.SeverityCritical,
		FilePath: "users.go", LineNumber: 2, Message: "was here",
		PriorFingerprint: "fp-fixed", Verdict: agents.VerdictFixed,
		Evidence: "current code no longer contains the query", Confidence: 0.9,
	}
	findings := []agents.AgentFinding{verdictFinding}
	p := newTestPipeline(t, gh, store, &findings)
	p.SetCloneURL(repoDir)

	p.Process(context.Background(), EventInput{RepoFullName: "o/r", PRNumber: 1, HeadSHA: commitSHA})

	gh.mu.Lock()
	resolved := append([]string(nil), gh.resolved...)
	last := gh.reviews[len(gh.reviews)-1]
	gh.mu.Unlock()
	if len(resolved) != 1 || resolved[0] != "t-fixed" {
		t.Errorf("resolved = %v, want [t-fixed]", resolved)
	}
	if last["event"] != "COMMENT" {
		t.Errorf("event = %v, want COMMENT", last["event"])
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
