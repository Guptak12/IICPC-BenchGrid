-- +goose Up
-- +goose NO TRANSACTION
-- Critical for 50K-scale leaderboard generation (ROW_NUMBER OVER PARTITION BY user_id ORDER BY composite_score)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_submissions_leaderboard 
ON submissions (arena_id, user_id, composite_score DESC);

-- Accelerates dashboard "active submissions" count query
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_submissions_status
ON submissions (status) WHERE status IN ('queued', 'building', 'running');

-- +goose Down
DROP INDEX IF EXISTS idx_submissions_leaderboard;
DROP INDEX IF EXISTS idx_submissions_status;
