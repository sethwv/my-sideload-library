-- +goose Up
CREATE TABLE user_connections (
    user_id INTEGER NOT NULL,
    provider TEXT NOT NULL,
    secret TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    last_success_at INTEGER NOT NULL DEFAULT 0,
    last_attempt_at INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, provider),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE connection_shelves (
    user_id INTEGER NOT NULL,
    provider TEXT NOT NULL,
    remote_key TEXT NOT NULL,
    name TEXT NOT NULL,
    selected INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, provider, remote_key),
    FOREIGN KEY (user_id, provider) REFERENCES user_connections(user_id, provider) ON DELETE CASCADE
);

CREATE TABLE connection_items (
    user_id INTEGER NOT NULL,
    provider TEXT NOT NULL,
    remote_shelf_key TEXT NOT NULL,
    external_id TEXT NOT NULL,
    title TEXT NOT NULL,
    author TEXT NOT NULL DEFAULT '',
    isbn TEXT NOT NULL DEFAULT '',
    added_at INTEGER NOT NULL DEFAULT 0,
    source_position INTEGER NOT NULL DEFAULT 0,
    local_book_id INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, provider, remote_shelf_key, external_id),
    FOREIGN KEY (user_id, provider) REFERENCES user_connections(user_id, provider) ON DELETE CASCADE
);
CREATE INDEX idx_connection_items_shelf ON connection_items(user_id, provider, remote_shelf_key, added_at DESC, source_position ASC);

-- +goose Down
DROP INDEX IF EXISTS idx_connection_items_shelf;
DROP TABLE IF EXISTS connection_items;
DROP TABLE IF EXISTS connection_shelves;
DROP TABLE IF EXISTS user_connections;
