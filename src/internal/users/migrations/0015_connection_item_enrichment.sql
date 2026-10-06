-- +goose Up
ALTER TABLE connection_items ADD COLUMN hardcover_id TEXT NOT NULL DEFAULT '';
ALTER TABLE connection_items ADD COLUMN enriched_title TEXT NOT NULL DEFAULT '';
ALTER TABLE connection_items ADD COLUMN enriched_author TEXT NOT NULL DEFAULT '';
ALTER TABLE connection_items ADD COLUMN cover_url TEXT NOT NULL DEFAULT '';
ALTER TABLE connection_items ADD COLUMN enrichment_status TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_connection_items_enrichment ON connection_items(local_book_id, enrichment_status);

-- +goose Down
DROP INDEX IF EXISTS idx_connection_items_enrichment;
