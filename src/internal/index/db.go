// Package index maintains a SQLite index of the EPUB library, built from a
// full directory scan and kept current via an fsnotify watcher. All
// browsing/sorting queries read from this index, never the filesystem.
package index

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/sethwv/my-sideload-library/internal/logging"
	"github.com/sethwv/my-sideload-library/internal/sqlite"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type DB struct {
	sql *sql.DB
}

// withTx runs fn atomically. Multi-table index changes use this instead of
// leaving locations, shelves, and canonical book rows partially updated when
// a later statement fails.
func (d *DB) withTx(fn func(*sql.Tx) error) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Open opens (creating if necessary) the SQLite index at dbPath and applies
// the schema via goose (migrations/*.sql, embedded in the binary —
// 0001_baseline.sql is a byte-for-byte copy of the CREATE TABLE IF NOT
// EXISTS statements this package used to apply directly; anything added
// after goose's adoption gets its own numbered migration file instead).
func Open(dbPath string) (*DB, error) {
	sdb, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc.org/sqlite doesn't support real concurrent writers; a single
	// connection avoids "database is locked" errors from Go's connection pool.
	sdb.SetMaxOpenConns(1)

	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(logging.NewGooseLogger("library"))
	if err := goose.SetDialect("sqlite3"); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.Up(sdb, "migrations"); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	db := &DB{sql: sdb}
	// These three predate goose's adoption and stay as-is: they upgrade
	// databases whose book_enrichment/books tables were created before the
	// columns they check for existed, which the goose baseline's
	// CREATE-TABLE-IF-NOT-EXISTS alone can't retrofit onto an
	// already-existing table. See sideload-library-y3h for why this wasn't
	// folded into individual historical migrations.
	if err := db.migrateEnrichmentStatusColumn(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := db.migrateBookEnrichmentTable(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := db.migrateBookEnrichmentColumns(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return db, nil
}

// migrateEnrichmentStatusColumn adds enrichment_status to a books table that
// predates it. CREATE TABLE IF NOT EXISTS in schema only applies to
// brand-new databases, so an existing index.db needs the column added in
// place — there's no migration framework here, just an additive,
// idempotent ALTER TABLE guarded by checking what columns already exist.
func (d *DB) migrateEnrichmentStatusColumn() error {
	columns, err := sqlite.Columns(d.sql, "books")
	if err != nil {
		return err
	}
	if !columns["enrichment_status"] {
		if _, err := d.sql.Exec(`ALTER TABLE books ADD COLUMN enrichment_status TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

// migrateBookEnrichmentTable creates book_enrichment for a database that
// predates the enrichment/EPUB-data split (schema's CREATE TABLE IF NOT
// EXISTS only helps brand-new databases), then backfills status from the old
// books.enrichment_status column so pending/done/no_match/error counts
// survive the upgrade. series/series_index/published_date are deliberately
// left blank in the new table: the books columns already hold whatever
// merged EPUB+Hardcover value is currently displayed, and leaving them there
// preserves current display until the next enrichment pass (or a manual
// reset) repopulates book_enrichment cleanly.
func (d *DB) migrateBookEnrichmentTable() error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS book_enrichment (
		    book_id         INTEGER PRIMARY KEY,
		    series          TEXT,
		    series_index    REAL,
		    published_date  TEXT,
		    status          TEXT NOT NULL DEFAULT '',
		    updated_at      INTEGER NOT NULL,
		    FOREIGN KEY (book_id) REFERENCES books(id) ON DELETE CASCADE
		)`); err != nil {
		return err
	}

	_, err := d.sql.Exec(`
		INSERT INTO book_enrichment (book_id, status, updated_at)
		SELECT id, enrichment_status, updated_at FROM books
		WHERE enrichment_status != '' AND id NOT IN (SELECT book_id FROM book_enrichment)`)
	return err
}

// migrateBookEnrichmentColumns adds the title/description/genres/publisher/
// pages/isbn/rating columns to a book_enrichment table that predates them
// (same additive, idempotent ALTER TABLE approach as
// migrateEnrichmentStatusColumn — CREATE TABLE IF NOT EXISTS only helps
// brand-new databases). Books already marked "done" under the pre-broadened
// schema won't automatically re-queue to pick these up; an admin "reset
// enrichment" is the documented way to force a full re-pass after upgrading.
func (d *DB) migrateBookEnrichmentColumns() error {
	_, err := sqlite.EnsureColumns(d.sql, "book_enrichment", []sqlite.Column{
		{Name: "title", DDL: "TEXT"},
		{Name: "description", DDL: "TEXT"},
		{Name: "genres", DDL: "TEXT"},
		{Name: "publisher", DDL: "TEXT"},
		{Name: "pages", DDL: "INTEGER"},
		{Name: "isbn", DDL: "TEXT"},
		{Name: "rating", DDL: "REAL"},
	})
	return err
}

func (d *DB) Close() error {
	return d.sql.Close()
}

// SetMeta upserts a single key/value pair in the meta table.
func (d *DB) SetMeta(key, value string) error {
	_, err := d.sql.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ClearLibrary removes the derived library index while preserving user shelves
// themselves. A subsequent scan recreates books and their enrichment queue.
func (d *DB) ClearLibrary() error {
	return d.withTx(func(tx *sql.Tx) error {
		for _, query := range []string{
			`DELETE FROM shelf_books`,
			`DELETE FROM book_enrichment`,
			`DELETE FROM book_locations`,
			`DELETE FROM books`,
			`DELETE FROM meta WHERE key IN ('last_scan_at', 'last_scan_duration_ms')`,
			`DELETE FROM chaptarr_catalog_cache`,
		} {
			if _, err := tx.Exec(query); err != nil {
				return err
			}
		}
		return nil
	})
}

// BackfillLibraryRoot tags any row left over from before multi-root support
// (library_root = ”) with root, the first configured LIBRARY_PATH entry at
// the time of upgrade. It preserves row IDs (and therefore shelves/
// enrichment), so it must run once, before the first post-upgrade Scan, or
// those rows would otherwise look "missing" from every configured root and
// get pruned/re-inserted with new IDs. A no-op once rows are tagged.
func (d *DB) BackfillLibraryRoot(root string) error {
	_, err := d.sql.Exec(`UPDATE books SET library_root = ? WHERE library_root = ''`, root)
	return err
}

// GetMeta returns the value for key and whether it was found.
func (d *DB) GetMeta(key string) (string, bool, error) {
	var value string
	err := d.sql.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}
