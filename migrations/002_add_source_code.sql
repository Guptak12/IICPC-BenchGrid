-- Migration 002: Add source_code column and create index for optimized leaderboard queries

ALTER TABLE submissions ADD COLUMN IF NOT EXISTS source_code TEXT;

-- Drop old index if it exists and create optimized status-based index
DROP INDEX IF EXISTS idx_submissions_leaderboard;

CREATE INDEX IF NOT EXISTS idx_submissions_leaderboard_optimized 
ON submissions (status, contestant_id, composite_score DESC, updated_at ASC);
