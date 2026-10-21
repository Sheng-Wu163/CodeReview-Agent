-- v1.8 (FR-49 / 17.4 / T52): persist the structured Harness outcome of
-- every executed agent, including tool-call fingerprints. Failed agent
-- results are stored here so execution differences can be investigated.
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS agent_results JSONB;
