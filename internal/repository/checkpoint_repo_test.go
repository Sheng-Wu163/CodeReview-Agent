package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// newTestCheckpointRepo connects to the shared test database and returns a
// checkpoint repository with a clean table.
func newTestCheckpointRepo(t *testing.T) (*CheckpointRepo, func()) {
	t.Helper()

	reviewRepo, closeDB := newTestRepo(t)
	ctx := context.Background()
	if _, err := reviewRepo.pool.Exec(ctx, "TRUNCATE harness_checkpoints RESTART IDENTITY"); err != nil {
		closeDB()
		t.Fatalf("truncate harness_checkpoints: %v", err)
	}
	return NewCheckpointRepo(reviewRepo.pool), closeDB
}

func TestCheckpointSaveAndGet(t *testing.T) {
	repo, closeDB := newTestCheckpointRepo(t)
	defer closeDB()
	ctx := context.Background()

	cp := &Checkpoint{
		RepoFullName: "o/r",
		PRNumber:     7,
		HeadSHA:      "sha-7",
		AgentName:    "security",
		Status:       "success",
		ResultJSON:   []byte(`{"status":"completed","tool_call_count":2}`),
	}
	if err := repo.Save(ctx, cp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if cp.ID == 0 {
		t.Error("Save should populate the generated ID")
	}

	got, err := repo.GetBySHAAndAgent(ctx, "sha-7", "security")
	if err != nil {
		t.Fatalf("GetBySHAAndAgent: %v", err)
	}
	if got.RepoFullName != "o/r" || got.PRNumber != 7 {
		t.Errorf("checkpoint identity = %+v", got)
	}
	if got.Status != "success" {
		t.Errorf("Status = %q, want success", got.Status)
	}
	if string(got.ResultJSON) == "" {
		t.Error("ResultJSON should be populated")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set by the database")
	}
}

func TestCheckpointSaveUpsertsOnSameSHAAndAgent(t *testing.T) {
	repo, closeDB := newTestCheckpointRepo(t)
	defer closeDB()
	ctx := context.Background()

	first := &Checkpoint{
		RepoFullName: "o/r", PRNumber: 7, HeadSHA: "sha-8",
		AgentName: "logic", Status: "failed", ResultJSON: []byte(`{"status":"failed"}`),
	}
	if err := repo.Save(ctx, first); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	second := &Checkpoint{
		RepoFullName: "o/r", PRNumber: 7, HeadSHA: "sha-8",
		AgentName: "logic", Status: "success", ResultJSON: []byte(`{"status":"completed"}`),
	}
	if err := repo.Save(ctx, second); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, err := repo.GetBySHAAndAgent(ctx, "sha-8", "logic")
	if err != nil {
		t.Fatalf("GetBySHAAndAgent: %v", err)
	}
	if got.Status != "success" {
		t.Errorf("Status = %q, want updated success", got.Status)
	}
	if string(got.ResultJSON) != `{"status": "completed"}` {
		t.Errorf("ResultJSON = %s, want updated payload", got.ResultJSON)
	}

	var count int
	if err := repo.pool.QueryRow(ctx,
		"SELECT count(*) FROM harness_checkpoints WHERE head_sha = $1", "sha-8",
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("checkpoint rows = %d, want 1 (unique constraint + upsert)", count)
	}
}

func TestCheckpointGetMissing(t *testing.T) {
	repo, closeDB := newTestCheckpointRepo(t)
	defer closeDB()

	_, err := repo.GetBySHAAndAgent(context.Background(), "missing-sha", "security")
	if err != pgx.ErrNoRows {
		t.Errorf("GetBySHAAndAgent error = %v, want pgx.ErrNoRows", err)
	}
}
