-- +goose Up
-- Migration 004: Add github_url column to submissions table
ALTER TABLE submissions ADD COLUMN IF NOT EXISTS github_url VARCHAR(2048);

-- +goose Down
ALTER TABLE submissions DROP COLUMN IF EXISTS github_url;
