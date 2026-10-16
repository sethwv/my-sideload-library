package index

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sethwv/my-sideload-library/internal/epub"
)

// CoverSaver persists a book's extracted cover image and returns a path
// (relative to the thumbnail cache dir) to store in cover_path. Implemented
// by internal/thumbnail; scan works fine with a nil saver (no thumbnails yet).
type CoverSaver interface {
	SaveCover(bookID int64, data []byte, mediaType string) (relPath string, err error)
}

// Scan walks each of libraryRoots for .epub files and upserts them into the
// index, then deletes rows for files that no longer exist in any of them.
// Per-file parse errors are logged and stored on the row rather than
// aborting the scan. Records last_scan_at/last_scan_duration_ms in the meta
// table on success. Files whose size/mtime haven't changed since the last
// scan are skipped without re-parsing; use Reimport to force a full re-parse.
func (d *DB) Scan(libraryRoots []string, saver CoverSaver) error {
	return d.scanAndRecord(libraryRoots, saver, false)
}

// Reimport is like Scan but re-parses every EPUB regardless of whether its
// file has changed, so an EPUB-metadata-parsing fix (e.g. improved
// identifier extraction) applies to books that are already indexed. Each
// book's added_at is preserved: the UPDATE path used for existing files
// never touches that column, only the INSERT path (new files) does.
func (d *DB) Reimport(libraryRoots []string, saver CoverSaver) error {
	return d.scanAndRecord(libraryRoots, saver, true)
}

func (d *DB) scanAndRecord(libraryRoots []string, saver CoverSaver, force bool) error {
	start := time.Now()
	if err := d.scan(libraryRoots, saver, force); err != nil {
		return err
	}

	_ = d.SetMeta("last_scan_at", strconv.FormatInt(time.Now().Unix(), 10))
	_ = d.SetMeta("last_scan_duration_ms", strconv.FormatInt(time.Since(start).Milliseconds(), 10))
	return nil
}

// seenKey identifies a file within a specific library root, so two roots
// that happen to contain the same relative path (e.g. "Author/Book.epub")
// aren't treated as the same book during pruning.
type seenKey struct {
	root string
	rel  string
}

func (d *DB) scan(libraryRoots []string, saver CoverSaver, force bool) error {
	seen := make(map[seenKey]bool)

	for _, root := range libraryRoots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				logger.Error("scan: walk error at")
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(entry.Name()), ".epub") {
				return nil
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			seen[seenKey{root, rel}] = true

			info, err := entry.Info()
			if err != nil {
				logger.Error("scan: stat error for")
				return nil
			}

			if err := d.upsertIfChanged(root, rel, path, info, saver, force); err != nil {
				logger.Error("scan: upsert error for")
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("walk library %s: %w", root, err)
		}
	}

	if err := d.pruneMissing(seen); err != nil {
		return fmt.Errorf("prune missing: %w", err)
	}

	if err := d.ConsolidateDuplicateTitles(libraryRoots); err != nil {
		return fmt.Errorf("consolidate duplicates: %w", err)
	}

	return nil
}

func (d *DB) upsertIfChanged(root, relPath, fullPath string, info fs.FileInfo, saver CoverSaver, force bool) error {
	size := info.Size()
	mtime := info.ModTime().Unix()

	var locBookID int64
	var locSize, locMtime int64
	lerr := d.sql.QueryRow(`SELECT book_id, file_size, file_mtime FROM book_locations WHERE library_root = ? AND file_path = ?`, root, relPath).
		Scan(&locBookID, &locSize, &locMtime)
	switch {
	case lerr == nil:
		if !force && locSize == size && locMtime == mtime {
			return nil // unchanged known-duplicate location, skip re-parsing
		}
		// This file isn't canonical for its book, so its own metadata never
		// drove title/author/etc. -- just refresh the stat used for future
		// unchanged-skip checks and pruning.
		_, err := d.sql.Exec(`UPDATE book_locations SET file_size=?, file_mtime=? WHERE library_root=? AND file_path=?`, size, mtime, root, relPath)
		return err
	case lerr != sql.ErrNoRows:
		return lerr
	}

	var existingID int64
	var existingSize, existingMtime int64
	err := d.sql.QueryRow(`SELECT id, file_size, file_mtime FROM books WHERE library_root = ? AND file_path = ?`, root, relPath).
		Scan(&existingID, &existingSize, &existingMtime)
	switch {
	case err == sql.ErrNoRows:
		// new file, proceed to parse+insert
	case err != nil:
		return err
	default:
		if !force && existingSize == size && existingMtime == mtime {
			return nil // unchanged, skip re-parsing
		}
	}

	now := time.Now().Unix()
	meta, parseErr := epub.ParseFile(fullPath)

	var title, sortTitle, author, sortAuthor, series, description, language, publisher, publishedDate, identifier string
	var seriesIndex float64
	var parseErrStr string

	if parseErr != nil {
		title = filepath.Base(relPath)
		sortTitle = strings.ToLower(title)
		parseErrStr = parseErr.Error()
		logger.Error("scan: failed to parse")
	} else {
		title = meta.Title
		sortTitle = meta.SortTitle
		author = meta.Author
		sortAuthor = meta.SortAuthor
		series = meta.Series
		seriesIndex = meta.SeriesIndex
		description = meta.Description
		language = meta.Language
		publisher = meta.Publisher
		publishedDate = meta.PublishedDate
		identifier = meta.Identifier
	}

	var coverPath string
	hasCover := parseErr == nil && meta.CoverData != nil

	if existingID != 0 {
		if hasCover && saver != nil {
			if p, err := saver.SaveCover(existingID, meta.CoverData, meta.CoverMediaType); err == nil {
				coverPath = p
			} else {
				logger.Error("scan: cover save failed for")
			}
		}
		_, err := d.sql.Exec(`
			UPDATE books SET library_root=?, file_size=?, file_mtime=?, title=?, sort_title=?, author=?, sort_author=?,
				series=?, series_index=?, description=?, language=?, publisher=?, published_date=?, identifier=?,
				cover_path=?, has_cover=?, updated_at=?, parse_error=?
			WHERE id=?`,
			root, size, mtime, title, sortTitle, author, sortAuthor,
			nullableString(series), nullableFloat(seriesIndex, series != ""), nullableString(description),
			nullableString(language), nullableString(publisher), nullableString(publishedDate), nullableString(identifier),
			nullableString(coverPath), boolToInt(hasCover), now, nullableString(parseErrStr),
			existingID,
		)
		return err
	}

	if parseErr == nil {
		if matchID, ok, ferr := d.findBookByTitleAuthor(title, author); ferr != nil {
			return ferr
		} else if ok {
			return d.insertLocation(matchID, root, relPath, size, mtime, saver, hasCover, meta)
		}
	}

	res, err := d.sql.Exec(`
		INSERT INTO books (library_root, file_path, file_size, file_mtime, title, sort_title, author, sort_author,
			series, series_index, description, language, publisher, published_date, identifier,
			cover_path, has_cover, added_at, updated_at, parse_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		root, relPath, size, mtime, title, sortTitle, author, sortAuthor,
		nullableString(series), nullableFloat(seriesIndex, series != ""), nullableString(description),
		nullableString(language), nullableString(publisher), nullableString(publishedDate), nullableString(identifier),
		nullableString(coverPath), boolToInt(hasCover), now, now, nullableString(parseErrStr),
	)
	if err != nil {
		return err
	}

	if hasCover && saver != nil {
		bookID, err := res.LastInsertId()
		if err == nil {
			if p, err := saver.SaveCover(bookID, meta.CoverData, meta.CoverMediaType); err == nil {
				_, _ = d.sql.Exec(`UPDATE books SET cover_path=? WHERE id=?`, p, bookID)
			} else {
				logger.Error("scan: cover save failed for")
			}
		}
	}

	return nil
}

func (d *DB) pruneMissing(seen map[seenKey]bool) error {
	rows, err := d.sql.Query(`SELECT id, library_root, file_path FROM books`)
	if err != nil {
		return err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		var root, path string
		if err := rows.Scan(&id, &root, &path); err != nil {
			rows.Close()
			return err
		}
		if !seen[seenKey{root, path}] {
			stale = append(stale, id)
		}
	}
	rows.Close()

	for _, id := range stale {
		if err := d.removeCanonicalFile(id, seen); err != nil {
			return err
		}
	}

	locRows, err := d.sql.Query(`SELECT library_root, file_path FROM book_locations`)
	if err != nil {
		return err
	}
	type loc struct{ root, path string }
	var staleLocs []loc
	for locRows.Next() {
		var l loc
		if err := locRows.Scan(&l.root, &l.path); err != nil {
			locRows.Close()
			return err
		}
		if !seen[seenKey{l.root, l.path}] {
			staleLocs = append(staleLocs, l)
		}
	}
	locRows.Close()
	for _, l := range staleLocs {
		if _, err := d.sql.Exec(`DELETE FROM book_locations WHERE library_root = ? AND file_path = ?`, l.root, l.path); err != nil {
			return err
		}
	}

	return nil
}

// removeCanonicalFile handles a book whose canonical file has disappeared.
// If it still has a known book_locations row, that location is promoted to
// canonical (preferring one confirmed present in seen, else the oldest
// known one) instead of deleting the book -- its shelves/enrichment
// survive. Only deletes the books row outright when no locations remain.
// seen may be nil (the fsnotify single-file-event path has no full-scan
// "seen" set to consult), in which case the oldest known location is used.
func (d *DB) removeCanonicalFile(bookID int64, seen map[seenKey]bool) error {
	rows, err := d.sql.Query(`
		SELECT library_root, file_path, file_size, file_mtime FROM book_locations
		WHERE book_id = ? ORDER BY added_at ASC`, bookID)
	if err != nil {
		return err
	}
	type loc struct {
		root, path  string
		size, mtime int64
	}
	var locs []loc
	for rows.Next() {
		var l loc
		if err := rows.Scan(&l.root, &l.path, &l.size, &l.mtime); err != nil {
			rows.Close()
			return err
		}
		locs = append(locs, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	promote := -1
	for i, l := range locs {
		if seen[seenKey{l.root, l.path}] {
			promote = i
			break
		}
	}
	if promote == -1 && len(locs) > 0 {
		promote = 0
	}

	if promote == -1 {
		_, err := d.sql.Exec(`DELETE FROM books WHERE id = ?`, bookID)
		return err
	}

	p := locs[promote]
	return d.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			UPDATE books SET library_root=?, file_path=?, file_size=?, file_mtime=?, updated_at=strftime('%s','now')
			WHERE id=?`, p.root, p.path, p.size, p.mtime, bookID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM book_locations WHERE library_root = ? AND file_path = ?`, p.root, p.path)
		return err
	})
}

// DeleteByPath removes the indexed row for a single file path within root,
// used by the fsnotify watcher. If the removed file was a book's canonical
// copy and another known location exists, promotes that location instead
// of deleting the book (see removeCanonicalFile).
func (d *DB) DeleteByPath(root, relPath string) error {
	var bookID int64
	err := d.sql.QueryRow(`SELECT id FROM books WHERE library_root = ? AND file_path = ?`, root, relPath).Scan(&bookID)
	if err == sql.ErrNoRows {
		_, err := d.sql.Exec(`DELETE FROM book_locations WHERE library_root = ? AND file_path = ?`, root, relPath)
		return err
	}
	if err != nil {
		return err
	}
	return d.removeCanonicalFile(bookID, nil)
}

// UpsertPath re-scans a single file within root, used by the fsnotify watcher.
func (d *DB) UpsertPath(root, relPath string, saver CoverSaver) error {
	fullPath := filepath.Join(root, relPath)
	info, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	return d.upsertIfChanged(root, relPath, fullPath, info, saver, false)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableFloat(f float64, has bool) any {
	if !has {
		return nil
	}
	return f
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
