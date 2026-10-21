// Package repository provides persistence of reviews and findings on top of
// PostgreSQL.
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Review is a record of one review run for a pull request (FR-14).
type Review struct {
	ID           int64
	RepoFullName string
	PRNumber     int
	HeadSHA      string
	Status       string // "pending", "completed", "failed"
	Summary      string
	// PreFindings holds the serialized static-analysis preFindings JSON
	// produced by the pre-analysis pipeline (v1.3 FR-24 / AC-43).
	PreFindings string
	// AgentStatus holds the serialized per-agent outcome map of the
	// adaptive strategy (v1.2 FR-37 / AC-77), e.g.
	// {"logic":"skipped_by_strategy"}.
	AgentStatus string
	// AgentResults holds the serialized structured Harness results of every
	// executed agent, including tool-call fingerprints (v1.8 FR-49 / 17.4).
	AgentResults string
	CreatedAt    time.Time
	UpdatedAt    *time.Time
}

// Finding is a single issue reported by a review agent.
type Finding struct {
	ID         int64
	ReviewID   int64
	Agent      string // e.g. "security", "logic", "performance", "lint"
	Severity   string // "critical", "warning", "suggestion"
	FilePath   string
	LineNumber int
	Message    string
	Suggestion string
	Confidence float64
	// FeedbackStatus is the v1.4 implicit feedback state of the finding
	// (FR-39): "pending", "accepted_implicit" or "ignored_implicit"
	// ("accepted_explicit"/"rejected_explicit" are reserved).
	FeedbackStatus string
}

// ReviewRepo implements review and finding persistence on a pgx connection
// pool. Per the project convention, consumers that need a narrower view of
// this type declare their own interface in their own package.
type ReviewRepo struct {
	pool *pgxpool.Pool
}

// NewReviewRepo creates a new ReviewRepo backed by pool.
func NewReviewRepo(pool *pgxpool.Pool) *ReviewRepo {
	return &ReviewRepo{pool: pool}
}

// CreateReview inserts a review and returns its generated ID.
func (r *ReviewRepo) CreateReview(ctx context.Context, rev *Review) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO reviews (repo_full_name, pr_number, head_sha, status, summary)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		rev.RepoFullName, rev.PRNumber, rev.HeadSHA, rev.Status, rev.Summary,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert review: %w", err)
	}
	return id, nil
}

// UpdateReviewPreFindings stores the serialized static-analysis preFindings
// JSON produced by the pre-analysis pipeline (v1.3 FR-24 / AC-43).
func (r *ReviewRepo) UpdateReviewPreFindings(ctx context.Context, id int64, preFindings string) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE reviews SET pre_findings = $2::jsonb
		WHERE id = $1`,
		id, preFindings,
	); err != nil {
		return fmt.Errorf("update review pre_findings: %w", err)
	}
	return nil
}

// UpdateReviewAgentStatus stores the per-agent outcome map of the adaptive
// strategy as JSON (v1.2 FR-37 / AC-77).
func (r *ReviewRepo) UpdateReviewAgentStatus(ctx context.Context, id int64, status map[string]string) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshal agent status: %w", err)
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE reviews SET agent_status = $2::jsonb
		WHERE id = $1`,
		id, string(data),
	); err != nil {
		return fmt.Errorf("update review agent_status: %w", err)
	}
	return nil
}

// UpdateReviewAgentResults stores the serialized structured Harness results
// (status, tool calls, execution logs) as JSON (v1.8 FR-49 / 17.4 / T52).
func (r *ReviewRepo) UpdateReviewAgentResults(ctx context.Context, id int64, results string) error {
	if results == "" {
		results = "{}"
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE reviews SET agent_results = $2::jsonb
		WHERE id = $1`,
		id, results,
	); err != nil {
		return fmt.Errorf("update review agent_results: %w", err)
	}
	return nil
}

// UpdateReviewStatus sets the status (and optionally the summary) of a
// review. Pass summary as nil to leave it unchanged.
func (r *ReviewRepo) UpdateReviewStatus(ctx context.Context, id int64, status, summary *string) error {
	var s any
	if summary != nil {
		s = *summary
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE reviews SET status = $1, summary = COALESCE($2, summary), updated_at = now()
		WHERE id = $3`, status, s, id); err != nil {
		return fmt.Errorf("update review %d status: %w", id, err)
	}
	return nil
}

// GetReview fetches a review by ID. It returns pgx.ErrNoRows when the review
// does not exist.
func (r *ReviewRepo) GetReview(ctx context.Context, id int64) (*Review, error) {
	var rev Review
	var updatedAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT id, repo_full_name, pr_number, head_sha, status, summary,
		       COALESCE(pre_findings::text, ''), COALESCE(agent_status::text, ''),
		       COALESCE(agent_results::text, ''), created_at, updated_at
		FROM reviews WHERE id = $1`, id,
	).Scan(&rev.ID, &rev.RepoFullName, &rev.PRNumber, &rev.HeadSHA,
		&rev.Status, &rev.Summary, &rev.PreFindings, &rev.AgentStatus,
		&rev.AgentResults, &rev.CreatedAt, &updatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("select review %d: %w", id, err)
	}
	rev.UpdatedAt = updatedAt
	return &rev, nil
}

// ListReviews returns up to limit reviews ordered by creation time
// descending (newest first, FR-15).
func (r *ReviewRepo) ListReviews(ctx context.Context, limit int) ([]Review, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, repo_full_name, pr_number, head_sha, status, summary,
		       COALESCE(pre_findings::text, ''), COALESCE(agent_status::text, ''),
		       COALESCE(agent_results::text, ''), created_at, updated_at
		FROM reviews ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	defer rows.Close()

	var reviews []Review
	for rows.Next() {
		var rev Review
		var updatedAt *time.Time
		if err := rows.Scan(&rev.ID, &rev.RepoFullName, &rev.PRNumber, &rev.HeadSHA,
			&rev.Status, &rev.Summary, &rev.PreFindings, &rev.AgentStatus,
			&rev.AgentResults, &rev.CreatedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan review row: %w", err)
		}
		rev.UpdatedAt = updatedAt
		reviews = append(reviews, rev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reviews: %w", err)
	}
	return reviews, nil
}

// CreateFinding inserts a finding and returns its generated ID.
func (r *ReviewRepo) CreateFinding(ctx context.Context, f *Finding) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO findings (review_id, agent, severity, file_path, line_number, message, suggestion, confidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		f.ReviewID, f.Agent, f.Severity, f.FilePath, f.LineNumber, f.Message, f.Suggestion, f.Confidence,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert finding: %w", err)
	}
	return id, nil
}

// ListFindings returns all findings of a review ordered by severity and
// insertion order (FR-15 detail page).
func (r *ReviewRepo) ListFindings(ctx context.Context, reviewID int64) ([]Finding, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, review_id, agent, severity, file_path, line_number, message, suggestion, confidence, feedback_status
		FROM findings
		WHERE review_id = $1
		ORDER BY CASE severity
			WHEN 'critical' THEN 0
			WHEN 'warning' THEN 1
			ELSE 2
		END, id`, reviewID)
	if err != nil {
		return nil, fmt.Errorf("list findings for review %d: %w", reviewID, err)
	}
	defer rows.Close()

	var findings []Finding
	for rows.Next() {
		var f Finding
		var suggestion *string
		if err := rows.Scan(&f.ID, &f.ReviewID, &f.Agent, &f.Severity, &f.FilePath,
			&f.LineNumber, &f.Message, &suggestion, &f.Confidence, &f.FeedbackStatus); err != nil {
			return nil, fmt.Errorf("scan finding row: %w", err)
		}
		if suggestion != nil {
			f.Suggestion = *suggestion
		}
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate findings: %w", err)
	}
	return findings, nil
}
