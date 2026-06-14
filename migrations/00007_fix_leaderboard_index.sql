-- +goose Up
-- Migration 004: Fix idx_submissions_leaderboard_v3 index row size overflow.
-- The previous version INCLUDEd `diagnostics` (JSONB) which can be arbitrarily
-- large. PostgreSQL btree max index row size is 2704 bytes — the JSONB payload
-- was pushing it to 3752 bytes. Drop and recreate without the JSONB column.

DROP INDEX IF EXISTS idx_submissions_leaderboard_v3;

-- Lean covering index: only fixed-size numeric/timestamp columns in INCLUDE.
-- Queries needing `diagnostics` will do a heap fetch (acceptable at leaderboard scale).
CREATE INDEX IF NOT EXISTS idx_submissions_leaderboard_v4
ON submissions (status, contestant_id, composite_score DESC, updated_at ASC)
INCLUDE (id, verdict, correctness_score, p50_us, p90_us, p99_us, actual_tps);

-- +goose Down
DROP INDEX IF EXISTS idx_submissions_leaderboard_v4;
CREATE INDEX IF NOT EXISTS idx_submissions_leaderboard_v3
ON submissions (status, contestant_id, composite_score DESC, updated_at ASC)
INCLUDE (id, verdict, correctness_score, p50_us, p90_us, p99_us, actual_tps, diagnostics);
