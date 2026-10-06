package index

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// EnrichmentCandidate is the minimal data needed to search Hardcover (or
// match against Chaptarr, by FilePath) for a book and decide whether to
// fill in blanks.
type EnrichmentCandidate struct {
	ID         int64
	Title      string
	Author     string
	Identifier string
	FilePath   string
	AddedAt    time.Time
}

// Enrichment source values stored in book_enrichment.source, surfaced on
// the Edit Metadata page so an admin can see which integration (if any)
// currently supplies a book's fields. SourceChaptarr takes precedence over
// SourceHardcover in the background queue (see internal/web's
// RunEnrichmentQueue/RunChaptarrQueue) — a book Chaptarr already claimed is
// never touched by the Hardcover pass, since both gate on the same
// needsEnrichmentWhere status=” check.
const (
	SourceHardcover = "hardcover"
	SourceChaptarr  = "chaptarr"
	SourceManual    = "manual"
)

// MetadataPatch is every metadata field an integration or an admin edit can
// contribute to a book. Blank/zero fields mean "leave the existing value
// alone", not "clear it". ApplyEnrichment and SaveMetadata deliberately use
// different merge policies for the same patch: provider data is conservative
// while an explicit admin edit always wins.
type MetadataPatch struct {
	Title         string
	Series        string
	SeriesIndex   float64
	PublishedDate string
	Description   string
	Genres        []string
	Publisher     string
	Pages         int
	ISBN          string
	Rating        float64
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// needsEnrichmentWhere gates both BooksNeedingEnrichment and
// GetEnrichmentStats: a book is still a candidate if it hasn't been checked
// yet (status = ”) and is missing any field Hardcover can contribute —
// series, release date, description, genres, publisher, page count, ISBN,
// rating, or a cover image. Kept as one shared fragment so the candidate
// query and the stats query can't drift apart.
const needsEnrichmentWhere = `COALESCE(be.status, '') = ''
	AND (
		COALESCE(be.series, b.series) IS NULL OR COALESCE(be.series, b.series) = ''
		OR COALESCE(be.published_date, b.published_date) IS NULL OR COALESCE(be.published_date, b.published_date) = ''
		OR COALESCE(be.description, b.description) IS NULL OR COALESCE(be.description, b.description) = ''
		OR be.genres IS NULL OR be.genres = ''
		OR COALESCE(be.publisher, b.publisher) IS NULL OR COALESCE(be.publisher, b.publisher) = ''
		OR be.pages IS NULL OR be.pages = 0
		OR be.isbn IS NULL OR be.isbn = ''
		OR be.rating IS NULL OR be.rating = 0
		OR b.has_cover = 0
	)`

// BooksNeedingEnrichment returns up to limit books that haven't been checked
// against Hardcover yet (enrichment status = ”) and are missing at least one
// field auto-fill can contribute (see needsEnrichmentWhere). Already
// `done`/`no_match`/`error` books are skipped so a restart resumes instead
// of reprocessing the whole library.
func (d *DB) BooksNeedingEnrichment(limit int) ([]EnrichmentCandidate, error) {
	rows, err := d.sql.Query(`
		SELECT b.id, b.title, b.author, COALESCE(b.identifier, ''), b.file_path, b.added_at FROM books b
		LEFT JOIN book_enrichment be ON be.book_id = b.id
		WHERE `+needsEnrichmentWhere+`
		ORDER BY b.id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list books needing enrichment: %w", err)
	}
	defer rows.Close()

	var out []EnrichmentCandidate
	for rows.Next() {
		var c EnrichmentCandidate
		var addedAt int64
		if err := rows.Scan(&c.ID, &c.Title, &c.Author, &c.Identifier, &c.FilePath, &addedAt); err != nil {
			return nil, err
		}
		c.AddedAt = time.Unix(addedAt, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// BooksMissingHardcoverID returns local books that can be linked to a known
// Chaptarr Hardcover ID without issuing a Hardcover API request.
func (d *DB) BooksMissingHardcoverID(limit int) ([]EnrichmentCandidate, error) {
	if limit < 1 {
		limit = 100
	}
	rows, err := d.sql.Query(`SELECT b.id, b.title, b.author, b.identifier, b.file_path, b.added_at
		FROM books b LEFT JOIN book_enrichment be ON be.book_id = b.id
		WHERE COALESCE(be.hardcover_id, '') = '' ORDER BY b.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var books []EnrichmentCandidate
	for rows.Next() {
		var candidate EnrichmentCandidate
		var addedAt int64
		if err := rows.Scan(&candidate.ID, &candidate.Title, &candidate.Author, &candidate.Identifier, &candidate.FilePath, &addedAt); err != nil {
			return nil, err
		}
		candidate.AddedAt = time.Unix(addedAt, 0)
		books = append(books, candidate)
	}
	return books, rows.Err()
}

// SetEnrichmentStatus records the outcome of an enrichment attempt for a
// book, so it isn't retried every scan/queue pass. Valid statuses: "done",
// "no_match", "error".
func (d *DB) SetEnrichmentStatus(bookID int64, status string) error {
	return setProviderStatus(d.sql, "status", bookID, status)
}

// SetChaptarrStatus records Chaptarr's own outcome for a book, independent
// of the combined status/source columns -- used by the admin "hide no
// Chaptarr match" filter. Valid statuses: "done", "no_match", "error".
func (d *DB) SetChaptarrStatus(bookID int64, status string) error {
	return setProviderStatus(d.sql, "chaptarr_status", bookID, status)
}

// SetHardcoverStatus records Hardcover's own outcome for a book, independent
// of the combined status/source columns -- used by the admin "hide no
// Hardcover match" filter. Valid statuses: "done", "no_match", "error".
func (d *DB) SetHardcoverStatus(bookID int64, status string) error {
	return setProviderStatus(d.sql, "hardcover_status", bookID, status)
}

// SetHardcoverID records the canonical Hardcover book ID after a confident
// provider match so account shelf sync can resolve the exact same work.
func (d *DB) SetHardcoverID(bookID int64, hardcoverID string) error {
	_, err := d.sql.Exec(`INSERT INTO book_enrichment (book_id, hardcover_id, updated_at) VALUES (?, ?, ?) ON CONFLICT(book_id) DO UPDATE SET hardcover_id = excluded.hardcover_id, updated_at = excluded.updated_at`, bookID, strings.TrimSpace(hardcoverID), time.Now().Unix())
	return err
}

func setProviderStatus(exec sqlExecer, column string, bookID int64, status string) error {
	switch column {
	case "status", "chaptarr_status", "hardcover_status":
	default:
		return fmt.Errorf("invalid enrichment status column %q", column)
	}
	_, err := exec.Exec(fmt.Sprintf(`
		INSERT INTO book_enrichment (book_id, %s, updated_at) VALUES (?, ?, strftime('%%s','now'))
		ON CONFLICT(book_id) DO UPDATE SET %s = excluded.%s, updated_at = excluded.updated_at`, column, column, column), bookID, status)
	return err
}

// EnrichmentStats summarizes enrichment progress for the admin page.
type EnrichmentStats struct {
	Pending int // still a candidate per needsEnrichmentWhere
	Done    int
	NoMatch int
	Errored int
}

func (d *DB) GetEnrichmentStats() (EnrichmentStats, error) {
	var s EnrichmentStats
	err := d.sql.QueryRow(`
		SELECT
			COUNT(*) FILTER (WHERE `+needsEnrichmentWhere+`),
			COUNT(*) FILTER (WHERE be.status = 'done'),
			COUNT(*) FILTER (WHERE be.status = 'no_match'),
			COUNT(*) FILTER (WHERE be.status = 'error')
		FROM books b
		LEFT JOIN book_enrichment be ON be.book_id = b.id`).Scan(&s.Pending, &s.Done, &s.NoMatch, &s.Errored)
	return s, err
}

// currentEnrichmentMerged returns the book_enrichment-over-books values a
// listing currently exposes. It uses the same MetadataPatch representation
// accepted by both write paths, so adding a field has one definition.
func currentEnrichmentMerged(q rowQuerier, bookID int64) (MetadataPatch, error) {
	var m MetadataPatch
	var title, series, publishedDate, description, genres, publisher, isbn sql.NullString
	var seriesIndex, rating sql.NullFloat64
	var pages sql.NullInt64
	err := q.QueryRow(`
		SELECT `+effectiveTitle+`, `+effectiveSeries+`, `+effectiveSeriesIndex+`, `+effectivePublishedDate+`,
			`+effectiveDescription+`, be.genres, `+effectivePublisher+`, be.pages, be.isbn, be.rating
		FROM books b LEFT JOIN book_enrichment be ON be.book_id = b.id
		WHERE b.id = ?`, bookID).Scan(&title, &series, &seriesIndex, &publishedDate, &description, &genres, &publisher, &pages, &isbn, &rating)
	if err != nil {
		return m, err
	}
	m.Title = title.String
	m.Series = series.String
	m.SeriesIndex = seriesIndex.Float64
	m.PublishedDate = publishedDate.String
	m.Description = description.String
	m.Genres = splitCSV(genres.String)
	m.Publisher = publisher.String
	m.Pages = int(pages.Int64)
	m.ISBN = isbn.String
	m.Rating = rating.Float64
	return m, nil
}

// GetEnrichmentSource returns the book_enrichment.source value for bookID
// ("hardcover"/"chaptarr"/"manual"/"" for not-yet-enriched), for display on
// the Edit Metadata page. Returns "" with no error if the book has no
// book_enrichment row yet.
func (d *DB) GetEnrichmentSource(bookID int64) (string, error) {
	var source sql.NullString
	err := d.sql.QueryRow(`SELECT source FROM book_enrichment WHERE book_id = ?`, bookID).Scan(&source)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return source.String, nil
}

func upsertEnrichment(exec sqlExecer, bookID int64, f MetadataPatch, status, source string) error {
	_, err := exec.Exec(`
		INSERT INTO book_enrichment (book_id, title, series, series_index, published_date, description, genres, publisher, pages, isbn, rating, status, source, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(book_id) DO UPDATE SET
			title = excluded.title,
			series = excluded.series,
			series_index = excluded.series_index,
			published_date = excluded.published_date,
			description = excluded.description,
			genres = excluded.genres,
			publisher = excluded.publisher,
			pages = excluded.pages,
			isbn = excluded.isbn,
			rating = excluded.rating,
			status = excluded.status,
			source = excluded.source,
			updated_at = excluded.updated_at`,
		bookID, nullIfEmpty(f.Title), nullIfEmpty(f.Series), f.SeriesIndex, nullIfEmpty(f.PublishedDate),
		nullIfEmpty(f.Description), nullIfEmpty(joinCSV(f.Genres)), nullIfEmpty(f.Publisher),
		nullIfZeroInt(int64(f.Pages)), nullIfEmpty(f.ISBN), nullIfZeroFloat(f.Rating), status, source)
	return err
}

// placeholderDescriptionMaxLen is the length below which an existing
// description is considered too thin to be worth keeping over Hardcover's
// (most EPUB "descriptions" that short are a blurb fragment, not real
// jacket copy).
const placeholderDescriptionMaxLen = 40

// descriptionIsPlaceholder reports whether cur is worth replacing with
// Hardcover's description even though it isn't blank — either it's short
// enough to be a stub, or it's just the book's own title repeated back.
func descriptionIsPlaceholder(cur, title string) bool {
	cur = strings.TrimSpace(cur)
	if cur == "" {
		return true
	}
	if len(cur) < placeholderDescriptionMaxLen {
		return true
	}
	if strings.EqualFold(cur, strings.TrimSpace(title)) {
		return true
	}
	return false
}

// ApplyEnrichment is the background queue's auto-fill path for a confident
// Hardcover match. Fields differ in how eagerly they trust Hardcover over
// what's already there:
//   - Title: always overwritten (Hardcover's title is trusted over the
//     EPUB's OPF metadata).
//   - Series/series index: overwritten if currently blank, or if Hardcover's
//     series differs from the current value — a confident match means
//     Hardcover's series/index is trusted over a stale or wrong EPUB value.
//   - Description: overwritten if blank or "placeholder-like" (short, or
//     just the title repeated), otherwise a substantial existing
//     description is left alone.
//   - Genres: always replaced with Hardcover's list when Hardcover returned
//     any (EPUB genre tags are rarely as good).
//   - Publisher/pages/isbn/rating: always take Hardcover's value when it has
//     one — EPUB OPF metadata for these is typically worse or absent.
//
// Also marks the book "done" with the given source (SourceHardcover or
// SourceChaptarr — whichever integration produced hc).
func (d *DB) ApplyEnrichment(bookID int64, patch MetadataPatch, source string) error {
	return d.withTx(func(tx *sql.Tx) error {
		cur, err := currentEnrichmentMerged(tx, bookID)
		if err != nil {
			return err
		}
		final := mergeProviderMetadata(cur, patch)
		if err := upsertEnrichment(tx, bookID, final, "done", source); err != nil {
			return err
		}
		switch source {
		case SourceChaptarr:
			return setProviderStatus(tx, "chaptarr_status", bookID, "done")
		case SourceHardcover:
			return setProviderStatus(tx, "hardcover_status", bookID, "done")
		}
		return nil
	})
}

func mergeProviderMetadata(cur, patch MetadataPatch) MetadataPatch {
	final := cur
	if patch.Title != "" {
		final.Title = patch.Title
	}
	if patch.Series != "" && (cur.Series == "" || !strings.EqualFold(cur.Series, patch.Series)) {
		final.Series, final.SeriesIndex = patch.Series, patch.SeriesIndex
	}
	if cur.PublishedDate == "" {
		final.PublishedDate = patch.PublishedDate
	}
	if patch.Description != "" && descriptionIsPlaceholder(cur.Description, final.Title) {
		final.Description = patch.Description
	}
	if len(patch.Genres) > 0 {
		final.Genres = patch.Genres
	}
	if patch.Publisher != "" {
		final.Publisher = patch.Publisher
	}
	if patch.Pages != 0 {
		final.Pages = patch.Pages
	}
	if patch.ISBN != "" {
		final.ISBN = patch.ISBN
	}
	if patch.Rating != 0 {
		final.Rating = patch.Rating
	}
	return final
}

// SaveMetadata unconditionally sets whichever non-blank/non-zero fields are
// present in f into book_enrichment — the manual, human-edited path (the
// Edit Metadata page), so unlike ApplyEnrichment there's no "only if
// currently blank/placeholder" guard: an admin typing a value into the form
// always wins. Title/series/etc. are written into book_enrichment (not
// directly to books) so they survive a later rescan, the same as
// ApplyEnrichment's automatic path. Also marks the book "done".
func (d *DB) SaveMetadata(bookID int64, patch MetadataPatch) error {
	return d.withTx(func(tx *sql.Tx) error {
		cur, err := currentEnrichmentMerged(tx, bookID)
		if err != nil {
			return err
		}
		return upsertEnrichment(tx, bookID, mergeManualMetadata(cur, patch), "done", SourceManual)
	})
}

func mergeManualMetadata(cur, patch MetadataPatch) MetadataPatch {
	final := cur
	if patch.Title != "" {
		final.Title = patch.Title
	}
	if patch.Series != "" {
		final.Series, final.SeriesIndex = patch.Series, patch.SeriesIndex
	}
	if patch.PublishedDate != "" {
		final.PublishedDate = patch.PublishedDate
	}
	if patch.Description != "" {
		final.Description = patch.Description
	}
	if len(patch.Genres) > 0 {
		final.Genres = patch.Genres
	}
	if patch.Publisher != "" {
		final.Publisher = patch.Publisher
	}
	if patch.Pages != 0 {
		final.Pages = patch.Pages
	}
	if patch.ISBN != "" {
		final.ISBN = patch.ISBN
	}
	if patch.Rating != 0 {
		final.Rating = patch.Rating
	}
	return final
}

// SetCover updates a book's cover image path directly on the books table
// (covers aren't part of book_enrichment — see internal/index/scan.go's
// scan-time cover write, which this mirrors), marking has_cover so the
// existing Cover handler serves it.
func (d *DB) SetCover(bookID int64, coverPath string) error {
	_, err := d.sql.Exec(`UPDATE books SET cover_path = ?, has_cover = 1 WHERE id = ?`, coverPath, bookID)
	return err
}

// ResetEnrichment clears all Hardcover-derived data for every book, without
// touching books' own EPUB-scanned data at all. BooksNeedingEnrichment then
// naturally picks every book back up on the queue's next pass.
func (d *DB) ResetEnrichment() error {
	_, err := d.sql.Exec(`DELETE FROM book_enrichment`)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZeroInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullIfZeroFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func joinCSV(items []string) string {
	return strings.Join(items, ",")
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
