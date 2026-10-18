package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"codereviewagent/internal/db"
)

// feedbackTestRepo bundles the repos needed by the feedback tests.
type feedbackTestRepo struct {
	*ReviewRepo
	*FeedbackRepo
}

// newFeedbackTestRepo connects to the test database, migrates it, truncates
// all tables and returns the repos plus a cleanup function.
func newFeedbackTestRepo(t *testing.T) (*feedbackTestRepo, func()) {
	t.Helper()
	if os.Getenv("SKIP_DB_TESTS") == "1" {
		t.Skip("SKIP_DB_TESTS is set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.Connect(ctx, testDatabaseURL)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate test database: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE pr_commits, findings, reviews RESTART IDENTITY CASCADE"); err != nil {
		pool.Close()
		t.Fatalf("truncate tables: %v", err)
	}
	reviewRepo := NewReviewRepo(pool)
	return &feedbackTestRepo{ReviewRepo: reviewRepo, FeedbackRepo: NewFeedbackRepo(pool)}, pool.Close
}

// createFeedbackFixture creates one completed review for the given repo/PR
// and returns its ID.
func createFeedbackFixture(t *testing.T, repo *ReviewRepo, repoFullName string, prNumber int) int64 {
	t.Helper()
	id, err := repo.CreateReview(context.Background(), &Review{
		RepoFullName: repoFullName,
		PRNumber:     prNumber,
		HeadSHA:      fmt.Sprintf("sha-%d", prNumber),
		Status:       "completed",
	})
	if err != nil {
		t.Fatalf("CreateReview fixture: %v", err)
	}
	return id
}

// TestFeedbackPendingQuery covers T39: findings are created as pending and
// the PR-scoped pending query returns only the unresolved ones.
func TestFeedbackPendingQuery(t *testing.T) {
	repo, cleanup := newFeedbackTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	reviewID := createFeedbackFixture(t, repo.ReviewRepo, "fb/query", 7)

	f1, err := repo.CreateFinding(ctx, &Finding{
		ReviewID: reviewID, Agent: "security", Severity: "critical",
		FilePath: "auth/login.go", LineNumber: 5, Message: "SQL injection", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("CreateFinding: %v", err)
	}
	if _, err := repo.CreateFinding(ctx, &Finding{
		ReviewID: reviewID, Agent: "logic", Severity: "warning",
		FilePath: "auth/login.go", LineNumber: 9, Message: "unhandled error", Confidence: 0.8,
	}); err != nil {
		t.Fatalf("CreateFinding 2: %v", err)
	}

	// AC-81: new findings default to pending.
	pending, err := repo.ListPendingFindingsByPR(ctx, "fb/query", 7)
	if err != nil {
		t.Fatalf("ListPendingFindingsByPR: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending findings = %d, want 2", len(pending))
	}
	for _, f := range pending {
		if f.FeedbackStatus != FeedbackPending {
			t.Errorf("finding %d status = %q, want pending", f.ID, f.FeedbackStatus)
		}
	}

	// Resolve one; it must disappear from the pending query.
	if updated, err := repo.UpdateFindingFeedbackStatus(ctx, f1, FeedbackAcceptedImplicit); err != nil || !updated {
		t.Fatalf("UpdateFindingFeedbackStatus: updated=%v err=%v", updated, err)
	}
	pending, err = repo.ListPendingFindingsByPR(ctx, "fb/query", 7)
	if err != nil {
		t.Fatalf("ListPendingFindingsByPR after resolve: %v", err)
	}
	if len(pending) != 1 || pending[0].Agent != "logic" {
		t.Fatalf("pending after resolve = %+v, want only the logic finding", pending)
	}
}

// TestUpdateFindingFeedbackStatusIdempotent covers the strict state machine
// (CLAUDE.v.1.4.md): a resolved record is never changed again.
func TestUpdateFindingFeedbackStatusIdempotent(t *testing.T) {
	repo, cleanup := newFeedbackTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	reviewID := createFeedbackFixture(t, repo.ReviewRepo, "fb/idem", 8)
	id, err := repo.CreateFinding(ctx, &Finding{
		ReviewID: reviewID, Agent: "security", Severity: "warning",
		FilePath: "a.go", LineNumber: 2, Message: "m", Confidence: 0.7,
	})
	if err != nil {
		t.Fatalf("CreateFinding: %v", err)
	}

	if updated, err := repo.UpdateFindingFeedbackStatus(ctx, id, FeedbackAcceptedImplicit); err != nil || !updated {
		t.Fatalf("first update: updated=%v err=%v", updated, err)
	}
	// Second transition attempt must be a no-op (idempotency guard).
	if updated, err := repo.UpdateFindingFeedbackStatus(ctx, id, FeedbackIgnoredImplicit); err != nil {
		t.Fatalf("second update: %v", err)
	} else if updated {
		t.Error("second update must not touch an already-resolved finding")
	}

	findings, err := repo.ListFindings(ctx, reviewID)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if findings[0].FeedbackStatus != FeedbackAcceptedImplicit {
		t.Errorf("status = %q, want accepted_implicit (unchanged)", findings[0].FeedbackStatus)
	}
}

// TestRecordPRCommitDedup covers pr_commits (FR-39): re-delivered webhooks
// must not duplicate rows.
func TestRecordPRCommitDedup(t *testing.T) {
	repo, cleanup := newFeedbackTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := repo.RecordPRCommit(ctx, "fb/commits", 9, "abc123"); err != nil {
			t.Fatalf("RecordPRCommit run %d: %v", i, err)
		}
	}
	commits, err := repo.ListPRCommits(ctx, "fb/commits", 9)
	if err != nil {
		t.Fatalf("ListPRCommits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("pr_commits rows = %d, want 1 (dedup)", len(commits))
	}
	if commits[0].RepoFullName != "fb/commits" || commits[0].PRNumber != 9 || commits[0].SHA != "abc123" {
		t.Errorf("commit = %+v", commits[0])
	}
	if commits[0].CreatedAt.IsZero() {
		t.Error("CreatedAt must be set by the database")
	}
}

// TestFeedbackStats covers FR-43: per-agent accept rate over resolved
// findings.
func TestFeedbackStats(t *testing.T) {
	repo, cleanup := newFeedbackTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	reviewID := createFeedbackFixture(t, repo.ReviewRepo, "fb/stats", 10)
	mk := func(agent, file string, line int) *Finding {
		return &Finding{ReviewID: reviewID, Agent: agent, Severity: "warning",
			FilePath: file, LineNumber: line, Message: "m", Confidence: 0.8}
	}
	// security: 3 findings, 2 accepted, 1 pending => rate 1.0 (2/2 resolved).
	ids := make([]int64, 0, 3)
	for i, f := range []*Finding{mk("security", "a.go", 1), mk("security", "b.go", 2), mk("security", "c.go", 3)} {
		id, err := repo.CreateFinding(ctx, f)
		if err != nil {
			t.Fatalf("CreateFinding %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	// logic: 2 findings, 1 accepted, 1 ignored => rate 0.5.
	logicIDs := make([]int64, 0, 2)
	for i, f := range []*Finding{mk("logic", "a.go", 5), mk("logic", "b.go", 6)} {
		id, err := repo.CreateFinding(ctx, f)
		if err != nil {
			t.Fatalf("CreateFinding logic %d: %v", i, err)
		}
		logicIDs = append(logicIDs, id)
	}

	_, _ = repo.UpdateFindingFeedbackStatus(ctx, ids[0], FeedbackAcceptedImplicit)
	_, _ = repo.UpdateFindingFeedbackStatus(ctx, ids[1], FeedbackAcceptedImplicit)
	_, _ = repo.UpdateFindingFeedbackStatus(ctx, logicIDs[0], FeedbackAcceptedImplicit)
	_, _ = repo.UpdateFindingFeedbackStatus(ctx, logicIDs[1], FeedbackIgnoredImplicit)

	stats, err := repo.FeedbackStats(ctx)
	if err != nil {
		t.Fatalf("FeedbackStats: %v", err)
	}
	byAgent := map[string]AgentFeedbackStat{}
	for _, s := range stats {
		byAgent[s.Agent] = s
	}
	sec := byAgent["security"]
	if sec.Total != 3 || sec.Accepted != 2 || sec.Pending != 1 {
		t.Errorf("security stats = %+v, want total=3 accepted=2 pending=1", sec)
	}
	if sec.AcceptRate != 1.0 {
		t.Errorf("security accept rate = %v, want 1.0", sec.AcceptRate)
	}
	logic := byAgent["logic"]
	if logic.AcceptRate != 0.5 {
		t.Errorf("logic accept rate = %v, want 0.5 (accepted/total_resolved)", logic.AcceptRate)
	}
}
