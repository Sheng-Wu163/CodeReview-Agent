-- v1.4 (FR-39 / AC-81): implicit feedback tracking.
-- findings.feedback_status records what the developer did with each
-- finding; pr_commits records the commit history of every PR.
ALTER TABLE findings ADD COLUMN IF NOT EXISTS feedback_status VARCHAR(32) NOT NULL DEFAULT 'pending';

CREATE TABLE IF NOT EXISTS pr_commits (
	id BIGSERIAL PRIMARY KEY,
	repo_full_name TEXT NOT NULL,
	pr_number INTEGER NOT NULL,
	sha TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (repo_full_name, pr_number, sha)
);
