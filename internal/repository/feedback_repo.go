package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Feedback status values stored in findings.feedback_status (v1.4 FR-39).
const (
	// FeedbackPending marks a finding whose outcome is not yet known.
	FeedbackPending = "pending"
	// FeedbackAcceptedImplicit marks a finding whose code was subsequently
	// modified (the developer acted on it).
	FeedbackAcceptedImplicit = "accepted_implicit"
	// FeedbackIgnoredImplicit marks a finding that stayed untouched while
	// the PR was merged.
	FeedbackIgnoredImplicit = "ignored_implicit"
	// FeedbackAcceptedExplicit and FeedbackRejectedExplicit are reserved
	// for future explicit feedback (FR-39).
	FeedbackAcceptedExplicit = "accepted_explicit"
	FeedbackRejectedExplicit = "rejected_explicit"
)

// AgentFeedbackStat is one row of the per-agent adoption statistics
// (v1.4 FR-43).
type AgentFeedbackStat struct {
	// Agent is the review agent name.
	Agent string
	// Total is the number of findings reported by the agent.
	Total int
	// Accepted is the number of accepted findings (implicit + explicit).
	Accepted int
	// Ignored is the number of ignored findings (implicit).
	Ignored int
	// Pending is the number of findings still awaiting an outcome.
	Pending int
	// AcceptRate is accepted / (accepted + ignored), i.e. accepted over
	// resolved; 0 when nothing is resolved yet.
	AcceptRate float64
}

// FeedbackRepo implements the v1.4 implicit feedback persistence (FR-39):
// pending-finding queries, guarded status transitions and the pr_commits
// history.
type FeedbackRepo struct {
	pool *pgxpool.Pool
}

// NewFeedbackRepo creates a FeedbackRepo backed by pool.
func NewFeedbackRepo(pool *pgxpool.Pool) *FeedbackRepo {
	return &FeedbackRepo{pool: pool}
}

// ListPendingFindingsByPR returns the findings of one PR that are still in
// the pending feedback state (FR-41 input).
func (r *FeedbackRepo) ListPendingFindingsByPR(ctx context.Context, repoFullName string, prNumber int) ([]Finding, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT f.id, f.review_id, f.agent, f.severity, f.file_path, f.line_number,
		       f.message, COALESCE(f.suggestion, ''), f.confidence, f.feedback_status
		FROM findings f
		JOIN reviews rv ON rv.id = f.review_id
		WHERE rv.repo_full_name = $1 AND rv.pr_number = $2 AND f.feedback_status = $3
		ORDER BY f.id`,
		repoFullName, prNumber, FeedbackPending)
	if err != nil {
		return nil, fmt.Errorf("list pending findings for %s#%d: %w", repoFullName, prNumber, err)
	}
	defer rows.Close()

	var findings []Finding
	for rows.Next() {
		var f Finding
		if err := rows.Scan(&f.ID, &f.ReviewID, &f.Agent, &f.Severity, &f.FilePath,
			&f.LineNumber, &f.Message, &f.Suggestion, &f.Confidence, &f.FeedbackStatus); err != nil {
			return nil, fmt.Errorf("scan pending finding row: %w", err)
		}
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending findings: %w", err)
	}
	return findings, nil
}

// UpdateFindingFeedbackStatus transitions one finding's feedback status.
// The UPDATE is guarded on feedback_status = 'pending', so a record that is
// already resolved is never changed again (spec 15.3 idempotency rule). It
// returns whether a pending row was found and updated.
func (r *FeedbackRepo) UpdateFindingFeedbackStatus(ctx context.Context, id int64, status string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE findings SET feedback_status = $2
		WHERE id = $1 AND feedback_status = $3`,
		id, status, FeedbackPending)
	if err != nil {
		return false, fmt.Errorf("update finding %d feedback status: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// RecordPRCommit records one commit of a PR. Duplicate SHAs are ignored
// (UNIQUE key), so re-delivered webhooks are safe.
func (r *FeedbackRepo) RecordPRCommit(ctx context.Context, repoFullName string, prNumber int, sha string) error {
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO pr_commits (repo_full_name, pr_number, sha)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`,
		repoFullName, prNumber, sha,
	); err != nil {
		return fmt.Errorf("record PR commit %s#%d %s: %w", repoFullName, prNumber, sha, err)
	}
	return nil
}

// PRCommit is one recorded commit of a pull request (v1.4 spec 15.3).
type PRCommit struct {
	// RepoFullName is the repository, e.g. "owner/repo".
	RepoFullName string
	// PRNumber is the pull request number.
	PRNumber int
	// SHA is the commit SHA.
	SHA string
	// CreatedAt is when the commit was recorded.
	CreatedAt time.Time
}

// ListPRCommits returns the recorded commit history of one PR, oldest
// first.
func (r *FeedbackRepo) ListPRCommits(ctx context.Context, repoFullName string, prNumber int) ([]PRCommit, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT repo_full_name, pr_number, sha, created_at
		FROM pr_commits
		WHERE repo_full_name = $1 AND pr_number = $2
		ORDER BY created_at, id`,
		repoFullName, prNumber)
	if err != nil {
		return nil, fmt.Errorf("list PR commits for %s#%d: %w", repoFullName, prNumber, err)
	}
	defer rows.Close()

	var commits []PRCommit
	for rows.Next() {
		var c PRCommit
		if err := rows.Scan(&c.RepoFullName, &c.PRNumber, &c.SHA, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pr_commit row: %w", err)
		}
		commits = append(commits, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pr_commits: %w", err)
	}
	return commits, nil
}

// FeedbackStats returns the per-agent adoption statistics over all findings
// (v1.4 FR-43): accepted / total_resolved per agent.
func (r *FeedbackRepo) FeedbackStats(ctx context.Context) ([]AgentFeedbackStat, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT agent,
		       COUNT(*) AS total,
		       SUM(CASE WHEN feedback_status LIKE 'accepted%' THEN 1 ELSE 0 END) AS accepted,
		       SUM(CASE WHEN feedback_status LIKE 'ignored%' THEN 1 ELSE 0 END) AS ignored,
		       SUM(CASE WHEN feedback_status = 'pending' THEN 1 ELSE 0 END) AS pending
		FROM findings
		GROUP BY agent
		ORDER BY agent`)
	if err != nil {
		return nil, fmt.Errorf("feedback stats: %w", err)
	}
	defer rows.Close()

	var stats []AgentFeedbackStat
	for rows.Next() {
		var s AgentFeedbackStat
		if err := rows.Scan(&s.Agent, &s.Total, &s.Accepted, &s.Ignored, &s.Pending); err != nil {
			return nil, fmt.Errorf("scan feedback stat row: %w", err)
		}
		if resolved := s.Accepted + s.Ignored; resolved > 0 {
			s.AcceptRate = float64(s.Accepted) / float64(resolved)
		}
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate feedback stats: %w", err)
	}
	return stats, nil
}
