-- 001_create_tables.sql: create the reviews and findings tables.

BEGIN;

CREATE TABLE IF NOT EXISTS reviews (
    id              BIGSERIAL PRIMARY KEY,
    repo_full_name  TEXT        NOT NULL,
    pr_number       INTEGER     NOT NULL,
    head_sha        TEXT        NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'pending',
    summary         TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_reviews_created_at ON reviews (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reviews_repo_pr ON reviews (repo_full_name, pr_number);

CREATE TABLE IF NOT EXISTS findings (
    id           BIGSERIAL PRIMARY KEY,
    review_id    BIGINT      NOT NULL REFERENCES reviews (id) ON DELETE CASCADE,
    agent        TEXT        NOT NULL,
    severity     TEXT        NOT NULL,
    file_path    TEXT        NOT NULL,
    line_number  INTEGER     NOT NULL,
    message      TEXT        NOT NULL,
    suggestion   TEXT,
    confidence   REAL        NOT NULL DEFAULT 0.0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_findings_review_id ON findings (review_id);

COMMIT;
