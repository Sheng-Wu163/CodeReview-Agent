-- v1.2 (FR-37 / AC-77): persist the per-agent outcome of a review so the
-- adaptive strategy's decisions (e.g. "skipped_by_strategy") are auditable.
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS agent_status JSONB;
