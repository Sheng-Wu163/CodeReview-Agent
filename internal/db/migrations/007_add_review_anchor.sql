-- v1.10 (FR-62 / FR-64 / T63): review anchors for incremental reviews and
-- prompt/config-aware checkpoints.
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS reviewed_sha TEXT;
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS base_sha TEXT;
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS scope TEXT;
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS scope_reason TEXT;

CREATE INDEX IF NOT EXISTS idx_reviews_repo_pr_status
    ON reviews (repo_full_name, pr_number, status, created_at DESC);

-- Checkpoints are now keyed by the agent's prompt/config identity.
ALTER TABLE harness_checkpoints ADD COLUMN IF NOT EXISTS prompt_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE harness_checkpoints ADD COLUMN IF NOT EXISTS config_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE harness_checkpoints
    DROP CONSTRAINT IF EXISTS harness_checkpoints_head_sha_agent_name_key;
ALTER TABLE harness_checkpoints
    DROP CONSTRAINT IF EXISTS harness_checkpoints_sha_agent_prompt_config_key;

-- Preserve existing v1.8.1 rows before tightening the status domain.
ALTER TABLE harness_checkpoints DROP CONSTRAINT IF EXISTS harness_checkpoints_status_check;
UPDATE harness_checkpoints SET status = 'completed' WHERE status = 'success';
ALTER TABLE harness_checkpoints
    ADD CONSTRAINT harness_checkpoints_status_check
    CHECK (status IN ('completed', 'partial', 'failed'));
ALTER TABLE harness_checkpoints
    ADD CONSTRAINT harness_checkpoints_sha_agent_prompt_config_key
    UNIQUE (head_sha, agent_name, prompt_hash, config_hash);
