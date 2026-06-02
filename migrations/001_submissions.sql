-- PostgreSQL schema for IICPC Scaled Benchmarking Platform

CREATE TABLE IF NOT EXISTS submissions (
    id VARCHAR(36) PRIMARY KEY,
    contestant_id VARCHAR(255) NOT NULL,
    contest_id VARCHAR(255) NOT NULL DEFAULT 'default',
    status VARCHAR(50) NOT NULL DEFAULT 'queued',
    verdict VARCHAR(50) NOT NULL DEFAULT 'Pending',
    diagnostics JSONB DEFAULT '{}'::jsonb,
    composite_score DOUBLE PRECISION DEFAULT 0.0,
    correctness_score DOUBLE PRECISION DEFAULT 0.0,
    p50_us BIGINT DEFAULT 0,
    p90_us BIGINT DEFAULT 0,
    p99_us BIGINT DEFAULT 0,
    actual_tps DOUBLE PRECISION DEFAULT 0.0,
    s3_path VARCHAR(512),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Index for generating the leaderboard quickly (fetching max score per contestant in a contest)
CREATE INDEX IF NOT EXISTS idx_submissions_leaderboard 
ON submissions (contest_id, contestant_id, composite_score DESC);

-- Trigger to update updated_at automatically
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

CREATE OR REPLACE TRIGGER update_submissions_updated_at
    BEFORE UPDATE ON submissions
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();
