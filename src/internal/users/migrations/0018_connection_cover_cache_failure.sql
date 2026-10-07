-- +goose Up
ALTER TABLE connection_items ADD COLUMN cover_cache_failed INTEGER NOT NULL DEFAULT 0;

-- +goose Down
-- SQLite cannot drop columns on older supported versions.
