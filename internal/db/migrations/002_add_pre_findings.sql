-- v1.3 (FR-24 / AC-43): persist the serialized static-analysis preFindings
-- JSON produced by the pre-analysis pipeline for later analysis.
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS pre_findings JSONB;
