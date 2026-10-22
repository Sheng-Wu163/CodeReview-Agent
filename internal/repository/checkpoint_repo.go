package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Checkpoint is the persisted result of one agent execution for one PR head
// SHA (v1.8.1 spec 19.3, FROZEN).
type Checkpoint struct {
	// ID is the database identifier.
	ID int `json:"id"`
	// RepoFullName is the repository, e.g. "owner/repo".
	RepoFullName string `json:"repo_full_name"`
	// PRNumber is the pull request number.
	PRNumber int `json:"pr_number"`
	// HeadSHA is the reviewed commit SHA.
	HeadSHA string `json:"head_sha"`
	// AgentName is the agent identifier, e.g. "security".
	AgentName string `json:"agent_name"`
	// Status is "success" or "failed".
	Status string `json:"status"`
	// ResultJSON is the serialized harness.AgentResult.
	ResultJSON []byte `json:"result_json"`
	// CreatedAt is the last write time.
	CreatedAt time.Time `json:"created_at"`
}

// CheckpointRepository persists and loads agent checkpoints (v1.8.1 spec
// 19.3, FROZEN).
type CheckpointRepository interface {
	// Save upserts a checkpoint keyed by (head_sha, agent_name).
	Save(ctx context.Context, cp *Checkpoint) error
	// GetBySHAAndAgent returns the checkpoint, or pgx.ErrNoRows when none
	// exists.
	GetBySHAAndAgent(ctx context.Context, sha string, agent string) (*Checkpoint, error)
}

// CheckpointRepo implements CheckpointRepository on a pgx pool.
type CheckpointRepo struct {
	pool *pgxpool.Pool
}

// Compile-time check that CheckpointRepo implements CheckpointRepository.
var _ CheckpointRepository = (*CheckpointRepo)(nil)

// NewCheckpointRepo creates a CheckpointRepo backed by pool.
func NewCheckpointRepo(pool *pgxpool.Pool) *CheckpointRepo {
	return &CheckpointRepo{pool: pool}
}

// Save implements CheckpointRepository using an idempotent upsert on
// (head_sha, agent_name).
func (r *CheckpointRepo) Save(ctx context.Context, cp *Checkpoint) error {
	if cp == nil {
		return fmt.Errorf("save checkpoint: nil checkpoint")
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO harness_checkpoints
			(repo_full_name, pr_number, head_sha, agent_name, status, result_json)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		ON CONFLICT (head_sha, agent_name) DO UPDATE
		SET repo_full_name = EXCLUDED.repo_full_name,
		    pr_number = EXCLUDED.pr_number,
		    status = EXCLUDED.status,
		    result_json = EXCLUDED.result_json,
		    created_at = now()
		RETURNING id`,
		cp.RepoFullName, cp.PRNumber, cp.HeadSHA, cp.AgentName, cp.Status, string(cp.ResultJSON),
	).Scan(&cp.ID)
	if err != nil {
		return fmt.Errorf("upsert checkpoint for %s/%s: %w", cp.HeadSHA, cp.AgentName, err)
	}
	return nil
}

// GetBySHAAndAgent implements CheckpointRepository.
func (r *CheckpointRepo) GetBySHAAndAgent(ctx context.Context, sha string, agent string) (*Checkpoint, error) {
	var cp Checkpoint
	err := r.pool.QueryRow(ctx, `
		SELECT id, repo_full_name, pr_number, head_sha, agent_name, status, result_json, created_at
		FROM harness_checkpoints
		WHERE head_sha = $1 AND agent_name = $2`, sha, agent,
	).Scan(&cp.ID, &cp.RepoFullName, &cp.PRNumber, &cp.HeadSHA, &cp.AgentName,
		&cp.Status, &cp.ResultJSON, &cp.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("select checkpoint %s/%s: %w", sha, agent, err)
	}
	return &cp, nil
}
