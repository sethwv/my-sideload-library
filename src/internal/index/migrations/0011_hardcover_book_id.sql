-- +goose Up
ALTER TABLE book_enrichment ADD COLUMN hardcover_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_book_enrichment_hardcover_id ON book_enrichment(hardcover_id) WHERE hardcover_id != '';

-- +goose Down
DROP INDEX IF EXISTS idx_book_enrichment_hardcover_id;
