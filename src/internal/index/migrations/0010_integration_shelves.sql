-- +goose Up
ALTER TABLE shelves ADD COLUMN kind TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE shelves ADD COLUMN integration_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE shelves ADD COLUMN integration_shelf_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_shelves_integration_identity
    ON shelves(username, integration_provider, integration_shelf_key)
    WHERE kind = 'integration';

-- +goose Down
DROP INDEX IF EXISTS idx_shelves_integration_identity;
