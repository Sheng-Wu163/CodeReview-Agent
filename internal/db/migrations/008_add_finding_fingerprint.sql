-- v1.11 (FR-66 / T68): stable finding identity used for comment
-- deduplication and review-thread closure.
ALTER TABLE findings ADD COLUMN IF NOT EXISTS fingerprint TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_findings_fingerprint ON findings (fingerprint);
