-- +goose Up
ALTER TABLE connection_items ADD COLUMN cover_path TEXT NOT NULL DEFAULT '';

-- +goose Down
-- SQLite cannot drop columns on older supported versions.
