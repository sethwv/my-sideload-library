-- +goose Up
ALTER TABLE book_enrichment ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE book_enrichment ADD COLUMN next_retry_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_book_enrichment_retry ON book_enrichment(status, next_retry_at);

-- +goose Down
DROP INDEX IF EXISTS idx_book_enrichment_retry;
