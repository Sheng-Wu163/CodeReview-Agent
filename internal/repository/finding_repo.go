package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"codereviewagent/internal/review"
)

// FindingRepo provides maintenance operations over persisted findings.
type FindingRepo struct {
	pool *pgxpool.Pool
}

// NewFindingRepo creates a FindingRepo backed by pool.
func NewFindingRepo(pool *pgxpool.Pool) *FindingRepo {
	return &FindingRepo{pool: pool}
}

// BackfillFingerprints computes the v1.11 fingerprint for every finding that
// does not have one yet (T68). It is idempotent and safe to run at startup.
func (r *FindingRepo) BackfillFingerprints(ctx context.Context) (int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, file_path, agent, message
		FROM findings
		WHERE fingerprint = ''`)
	if err != nil {
		return 0, fmt.Errorf("select findings without fingerprint: %w", err)
	}
	defer rows.Close()

	type pending struct {
		id       int64
		filePath string
		agent    string
		message  string
	}
	var items []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.filePath, &p.agent, &p.message); err != nil {
			return 0, fmt.Errorf("scan finding for backfill: %w", err)
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate findings for backfill: %w", err)
	}

	updated := 0
	for _, p := range items {
		fp := review.FindingFingerprint(p.filePath, p.agent, p.message)
		if _, err := r.pool.Exec(ctx, `
			UPDATE findings SET fingerprint = $2
			WHERE id = $1 AND fingerprint = ''`, p.id, fp); err != nil {
			return updated, fmt.Errorf("backfill finding %d fingerprint: %w", p.id, err)
		}
		updated++
	}
	return updated, nil
}
