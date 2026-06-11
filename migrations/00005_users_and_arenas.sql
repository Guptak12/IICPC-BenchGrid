-- +goose Up

-- Users table
CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(36) PRIMARY KEY,
    handle VARCHAR(64) UNIQUE NOT NULL,
    email VARCHAR(255) UNIQUE,
    password_hash VARCHAR(255),            -- null for pure OAuth users
    github_id VARCHAR(64) UNIQUE,          -- GitHub OAuth ID
    role VARCHAR(20) DEFAULT 'contestant', -- contestant | admin
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Arenas (contests)
CREATE TABLE IF NOT EXISTS arenas (
    id VARCHAR(36) PRIMARY KEY,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    status VARCHAR(20) DEFAULT 'upcoming', -- upcoming | active | system_test | ended
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Contest registrations
CREATE TABLE IF NOT EXISTS arena_registrations (
    arena_id VARCHAR(36) REFERENCES arenas(id) ON DELETE CASCADE,
    user_id VARCHAR(36) REFERENCES users(id) ON DELETE CASCADE,
    registered_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (arena_id, user_id)
);

-- Update submissions table
ALTER TABLE submissions ADD COLUMN IF NOT EXISTS user_id VARCHAR(36) REFERENCES users(id);
ALTER TABLE submissions ADD COLUMN IF NOT EXISTS arena_id VARCHAR(36) REFERENCES arenas(id);

-- Performance Indexes
CREATE INDEX IF NOT EXISTS idx_submissions_arena_status ON submissions(arena_id, status);
CREATE INDEX IF NOT EXISTS idx_submissions_user_arena ON submissions(user_id, arena_id);

-- +goose Down
DROP INDEX IF EXISTS idx_submissions_user_arena;
DROP INDEX IF EXISTS idx_submissions_arena_status;
ALTER TABLE submissions DROP COLUMN IF EXISTS arena_id;
ALTER TABLE submissions DROP COLUMN IF EXISTS user_id;
DROP TABLE IF EXISTS arena_registrations;
DROP TABLE IF EXISTS arenas;
DROP TABLE IF EXISTS users;
