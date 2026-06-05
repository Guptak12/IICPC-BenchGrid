-- +goose Up
-- Migration 003: Move source code to a separate table for TOAST optimization

-- 1. Create dedicated source storage table
CREATE TABLE IF NOT EXISTS submission_sources (
    submission_id VARCHAR(36) PRIMARY KEY REFERENCES submissions(id) ON DELETE CASCADE,
    source_code TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

-- 2. Migrate existing data
INSERT INTO submission_sources (submission_id, source_code, created_at)
SELECT id, source_code, created_at FROM submissions
WHERE source_code IS NOT NULL
ON CONFLICT (submission_id) DO NOTHING;

-- 3. Drop source_code from hot table
ALTER TABLE submissions DROP COLUMN IF EXISTS source_code;

-- 4. Add GIN index on diagnostics JSONB for dashboard queries
CREATE INDEX IF NOT EXISTS idx_submissions_diagnostics_gin
ON submissions USING gin (diagnostics jsonb_path_ops);

-- 5. Partial index for active submissions (queued/compiling/running)
CREATE INDEX IF NOT EXISTS idx_submissions_active
ON submissions (created_at DESC)
WHERE status IN ('queued', 'compiling', 'running');

-- 6. Covering index for leaderboard query
DROP INDEX IF EXISTS idx_submissions_leaderboard_optimized;
CREATE INDEX IF NOT EXISTS idx_submissions_leaderboard_v3
ON submissions (status, contestant_id, composite_score DESC, updated_at ASC)
INCLUDE (id, verdict, correctness_score, p50_us, p90_us, p99_us, actual_tps, diagnostics);

-- +goose Down
DROP INDEX IF EXISTS idx_submissions_leaderboard_v3;
DROP INDEX IF EXISTS idx_submissions_active;
DROP INDEX IF EXISTS idx_submissions_diagnostics_gin;
ALTER TABLE submissions ADD COLUMN IF NOT EXISTS source_code TEXT;
UPDATE submissions s SET source_code = ss.source_code
FROM submission_sources ss WHERE s.id = ss.submission_id;
DROP TABLE IF EXISTS submission_sources;
CREATE INDEX IF NOT EXISTS idx_submissions_leaderboard_optimized 
ON submissions (status, contestant_id, composite_score DESC, updated_at ASC);
