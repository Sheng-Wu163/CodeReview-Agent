-- v1.8.1 (FR-57 / T59): per-agent execution checkpoints for resumable
-- reviews. A completed agent result is reused for the same head SHA unless
-- a forced rerun is requested.
CREATE TABLE IF NOT EXISTS harness_checkpoints (
    id             BIGSERIAL PRIMARY KEY,
    repo_full_name TEXT        NOT NULL,
    pr_number      INTEGER     NOT NULL,
    head_sha       TEXT        NOT NULL,
    agent_name     TEXT        NOT NULL,
    status         TEXT        NOT NULL CHECK (status IN ('success', 'failed')),
    result_json    JSONB       NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (head_sha, agent_name)
);

CREATE INDEX IF NOT EXISTS idx_harness_checkpoints_sha_agent
    ON harness_checkpoints (head_sha, agent_name);
