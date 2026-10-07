-- +goose Up
ALTER TABLE connection_items ADD COLUMN enrichment_retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE connection_items ADD COLUMN enrichment_next_retry_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_connection_items_enrichment_retry ON connection_items(local_book_id, enrichment_status, enrichment_next_retry_at);

-- +goose Down
DROP INDEX IF EXISTS idx_connection_items_enrichment_retry;
