package index

import (
	"path/filepath"
	"testing"
)

func TestBooksNeedingEnrichment_OnlyMissingFieldsUnprocessed(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "No Series Or Date", "Amy Zed")
	writeTestEpubWithSeries(t, filepath.Join(libDir, "b2.epub"), "Has Series Only", "Amy Zed", "A Saga", 1)
	writeTestEpubWithDate(t, filepath.Join(libDir, "b3.epub"), "Has Date Only", "Amy Zed", "2020-01-01")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}

	// All three are candidates: each is missing at least one of
	// series/published_date (auto-fill's "either field blank" test), even
	// though each has the *other* field already filled.
	candidates, err := db.BooksNeedingEnrichment(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("got %d candidates, want 3 (each missing at least one field): %+v", len(candidates), candidates)
	}

	if err := db.SetEnrichmentStatus(candidates[0].ID, "done"); err != nil {
		t.Fatal(err)
	}
	candidates, err = db.BooksNeedingEnrichment(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Errorf("got %d candidates after marking one done, want 2", len(candidates))
	}
}

func TestBooksMissingHardcoverIDAllowsNullIdentifier(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "book.epub"), "No Identifier", "Author")
	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE books SET identifier = NULL`); err != nil {
		t.Fatal(err)
	}
	candidates, err := db.BooksMissingHardcoverID(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Identifier != "" {
		t.Fatalf("candidates = %+v, want empty identifier", candidates)
	}
}

func TestBooksNeedingEnrichment_RetriesErrorsOnlyWhenDue(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Retry Me", "Amy Zed")
	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	candidates, err := db.BooksNeedingEnrichment(10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("initial candidates = %#v, %v", candidates, err)
	}
	bookID := candidates[0].ID
	if err := db.SetEnrichmentRetry(bookID); err != nil {
		t.Fatal(err)
	}
	if candidates, err = db.BooksNeedingEnrichment(10); err != nil || len(candidates) != 0 {
		t.Fatalf("backed off candidates = %#v, %v; want none", candidates, err)
	}
	if _, err := db.sql.Exec(`UPDATE book_enrichment SET next_retry_at = 0 WHERE book_id = ?`, bookID); err != nil {
		t.Fatal(err)
	}
	if candidates, err = db.BooksNeedingEnrichment(10); err != nil || len(candidates) != 1 {
		t.Fatalf("due retry candidates = %#v, %v; want one", candidates, err)
	}
}

// TestBooksNeedingEnrichment_ChaptarrClaimIsNeverRevisitedByHardcover locks
// in the mechanism RunChaptarrQueue/RunEnrichmentQueue (internal/web) both
// rely on for "Chaptarr takes precedence when both integrations are
// enabled": a book ApplyEnrichment marks done via SourceChaptarr is a
// book_enrichment row with status="done", which BooksNeedingEnrichment's
// needsEnrichmentWhere gate (status = ”) excludes — so Hardcover's own
// independent queue, which pulls its candidates from the same
// BooksNeedingEnrichment, can never re-process a book Chaptarr already
// claimed, without either queue needing to know the other exists.
func TestBooksNeedingEnrichment_ChaptarrClaimIsNeverRevisitedByHardcover(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Claimed By Chaptarr", "Amy Zed")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	candidates, err := db.BooksNeedingEnrichment(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates before any enrichment, want 1", len(candidates))
	}

	if err := db.ApplyEnrichment(id, MetadataPatch{Title: "Claimed By Chaptarr"}, SourceChaptarr); err != nil {
		t.Fatal(err)
	}

	candidates, err = db.BooksNeedingEnrichment(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Errorf("got %d candidates after a Chaptarr claim, want 0 (Hardcover's queue must never revisit a book Chaptarr already claimed)", len(candidates))
	}

	source, err := db.GetEnrichmentSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if source != SourceChaptarr {
		t.Errorf("GetEnrichmentSource = %q, want %q", source, SourceChaptarr)
	}
}

func TestGetEnrichmentStats(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Book One", "Amy Zed")
	writeTestEpub(t, filepath.Join(libDir, "b2.epub"), "Book Two", "Amy Zed")
	writeTestEpub(t, filepath.Join(libDir, "b3.epub"), "Book Three", "Amy Zed")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}

	candidates, err := db.BooksNeedingEnrichment(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("got %d candidates, want 3", len(candidates))
	}
	if err := db.SetEnrichmentStatus(candidates[0].ID, "done"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEnrichmentStatus(candidates[1].ID, "no_match"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEnrichmentStatus(candidates[2].ID, "error"); err != nil {
		t.Fatal(err)
	}

	stats, err := db.GetEnrichmentStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 0 || stats.Done != 1 || stats.NoMatch != 1 || stats.Errored != 1 {
		t.Errorf("stats = %+v, want {Pending:0 Done:1 NoMatch:1 Errored:1}", stats)
	}
}

func TestRetryEnrichmentStatus(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Book One", "Amy Zed")
	writeTestEpub(t, filepath.Join(libDir, "b2.epub"), "Book Two", "Amy Zed")
	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	candidates, err := db.BooksNeedingEnrichment(10)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetEnrichmentStatus(candidates[0].ID, "no_match"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEnrichmentStatus(candidates[1].ID, "error"); err != nil {
		t.Fatal(err)
	}
	if err := db.RetryEnrichmentStatus("no_match"); err != nil {
		t.Fatal(err)
	}
	stats, err := db.GetEnrichmentStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.NoMatch != 0 || stats.Errored != 1 {
		t.Errorf("stats after retry = %+v, want one pending no-match retry and one error", stats)
	}
	if err := db.RetryEnrichmentStatus("invalid"); err == nil {
		t.Error("RetryEnrichmentStatus accepted an invalid status")
	}
}

func TestApplyEnrichment_OverwritesTitleSeriesAndAlwaysFillableFields(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpubWithSeries(t, filepath.Join(libDir, "b1.epub"), "Has Series Already", "Amy Zed", "Existing Saga", 2)

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	if err := db.ApplyEnrichment(id, MetadataPatch{
		Title:         "Hardcover Title",
		Series:        "New Series",
		SeriesIndex:   9,
		PublishedDate: "2021-06-01",
		Description:   "A description from Hardcover",
		Genres:        []string{"Fantasy", "Adventure"},
		Publisher:     "Some Press",
		Pages:         321,
		ISBN:          "9781234567897",
		Rating:        4.2,
	}, SourceHardcover); err != nil {
		t.Fatal(err)
	}

	got, err := db.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Hardcover Title" {
		t.Errorf("Title = %q, want the Hardcover title (title is always overwritten on a confident match)", got.Title)
	}
	if got.Series != "New Series" || got.SeriesIndex != 9 {
		t.Errorf("expected series to be overwritten since it differs from Hardcover's, got Series=%q SeriesIndex=%v", got.Series, got.SeriesIndex)
	}
	if got.PublishedAt != "2021-06-01" {
		t.Errorf("PublishedAt = %q, want the fill-in value since it was blank", got.PublishedAt)
	}
	if got.Description != "A description from Hardcover" {
		t.Errorf("Description = %q, want the fill-in value since it was blank", got.Description)
	}
	if got.Publisher != "Some Press" {
		t.Errorf("Publisher = %q, want Hardcover's publisher (always preferred)", got.Publisher)
	}
	if got.Pages != 321 || got.ISBN != "9781234567897" || got.Rating != 4.2 {
		t.Errorf("got Pages=%d ISBN=%q Rating=%v, want Hardcover's values (always preferred)", got.Pages, got.ISBN, got.Rating)
	}
	if len(got.Genres) != 2 || got.Genres[0] != "Fantasy" || got.Genres[1] != "Adventure" {
		t.Errorf("Genres = %v, want [Fantasy Adventure]", got.Genres)
	}

	source, err := db.GetEnrichmentSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if source != SourceHardcover {
		t.Errorf("GetEnrichmentSource = %q, want %q", source, SourceHardcover)
	}
}

func TestApplyEnrichment_RecordsChaptarrSource(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Chaptarr Sourced Book", "Amy Zed")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	if err := db.ApplyEnrichment(id, MetadataPatch{Title: "Chaptarr Sourced Book"}, SourceChaptarr); err != nil {
		t.Fatal(err)
	}

	source, err := db.GetEnrichmentSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if source != SourceChaptarr {
		t.Errorf("GetEnrichmentSource = %q, want %q", source, SourceChaptarr)
	}
}

func TestApplyEnrichment_RollsBackWhenProviderStatusFails(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Original Title", "Amy Zed")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID
	if _, err := db.sql.Exec(`
		CREATE TRIGGER reject_chaptarr_status BEFORE UPDATE ON book_enrichment
		WHEN NEW.chaptarr_status = 'done'
		BEGIN SELECT RAISE(ABORT, 'forced provider status failure'); END`); err != nil {
		t.Fatal(err)
	}

	if err := db.ApplyEnrichment(id, MetadataPatch{Title: "Provider Title"}, SourceChaptarr); err == nil {
		t.Fatal("ApplyEnrichment succeeded despite forced provider status failure")
	}

	book, err := db.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if book.Title != "Original Title" {
		t.Errorf("title = %q, want original title after rollback", book.Title)
	}
	source, err := db.GetEnrichmentSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if source != "" {
		t.Errorf("source = %q, want no enrichment row after rollback", source)
	}
}

func TestSaveMetadata_RecordsManualSource(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Manually Edited Book", "Amy Zed")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	if err := db.SaveMetadata(id, MetadataPatch{Title: "Edited"}); err != nil {
		t.Fatal(err)
	}

	source, err := db.GetEnrichmentSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if source != SourceManual {
		t.Errorf("GetEnrichmentSource = %q, want %q", source, SourceManual)
	}
}

func TestGetEnrichmentSource_NoRowYet(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Never Enriched Book", "Amy Zed")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}

	source, err := db.GetEnrichmentSource(books[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if source != "" {
		t.Errorf("GetEnrichmentSource = %q, want empty for a book with no book_enrichment row", source)
	}
}

func TestApplyEnrichment_KeepsMatchingSeriesUnchanged(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpubWithSeries(t, filepath.Join(libDir, "b1.epub"), "Same Series Book", "Amy Zed", "Existing Saga", 2)

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	if err := db.ApplyEnrichment(id, MetadataPatch{
		Title:       "Same Series Book",
		Series:      "Existing Saga",
		SeriesIndex: 2,
	}, SourceHardcover); err != nil {
		t.Fatal(err)
	}

	got, err := db.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Series != "Existing Saga" || got.SeriesIndex != 2 {
		t.Errorf("got Series=%q SeriesIndex=%v, want it left as-is since Hardcover agrees", got.Series, got.SeriesIndex)
	}
}

func TestApplyEnrichment_DescriptionKeptWhenSubstantial(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Book With A Real Description", "Amy Zed")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	longDescription := "This is a substantial, real description of the book that is definitely longer than the placeholder threshold."
	if err := db.SaveMetadata(id, MetadataPatch{Description: longDescription}); err != nil {
		t.Fatal(err)
	}

	if err := db.ApplyEnrichment(id, MetadataPatch{
		Title:       "Book With A Real Description",
		Description: "Hardcover's alternate description",
	}, SourceHardcover); err != nil {
		t.Fatal(err)
	}

	got, err := db.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != longDescription {
		t.Errorf("Description = %q, want the existing substantial description left alone", got.Description)
	}
}

func TestApplyEnrichment_DescriptionReplacedWhenPlaceholder(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Book With A Stub Description", "Amy Zed")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	if err := db.SaveMetadata(id, MetadataPatch{Description: "Too short"}); err != nil {
		t.Fatal(err)
	}

	if err := db.ApplyEnrichment(id, MetadataPatch{
		Title:       "Book With A Stub Description",
		Description: "Hardcover's real, much longer description of the book",
	}, SourceHardcover); err != nil {
		t.Fatal(err)
	}

	got, err := db.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "Hardcover's real, much longer description of the book" {
		t.Errorf("Description = %q, want the placeholder-length description replaced", got.Description)
	}
}

func TestSaveMetadata_OverwritesExisting(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpubWithSeries(t, filepath.Join(libDir, "b1.epub"), "Old Title", "Amy Zed", "Old Saga", 1)

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	if err := db.SaveMetadata(id, MetadataPatch{
		Title:         "New Title",
		Series:        "New Saga",
		SeriesIndex:   5,
		PublishedDate: "2022-03-01",
		Publisher:     "New Press",
		Pages:         200,
		ISBN:          "9781234567897",
		Rating:        4.5,
		Genres:        []string{"Sci-Fi"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := db.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "New Title" || got.Series != "New Saga" || got.SeriesIndex != 5 || got.PublishedAt != "2022-03-01" {
		t.Errorf("got %+v, want overridden fields to all be applied", got)
	}
	if got.Publisher != "New Press" || got.Pages != 200 || got.ISBN != "9781234567897" || got.Rating != 4.5 {
		t.Errorf("got %+v, want the extended fields all applied too", got)
	}
	if len(got.Genres) != 1 || got.Genres[0] != "Sci-Fi" {
		t.Errorf("Genres = %v, want [Sci-Fi]", got.Genres)
	}
}

func TestSaveMetadata_BlankFieldsLeftAlone(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpubWithSeries(t, filepath.Join(libDir, "b1.epub"), "Keep This Title", "Amy Zed", "Keep Saga", 1)

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	// Zero values mean "the admin didn't choose to override this field".
	if err := db.SaveMetadata(id, MetadataPatch{PublishedDate: "2022-03-01"}); err != nil {
		t.Fatal(err)
	}

	got, err := db.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Keep This Title" || got.Series != "Keep Saga" || got.SeriesIndex != 1 {
		t.Errorf("got %+v, want title/series/index left alone", got)
	}
	if got.PublishedAt != "2022-03-01" {
		t.Errorf("PublishedAt = %q, want the one field that was set", got.PublishedAt)
	}
}

func TestSetCover_UpdatesPathAndHasCover(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Coverless Book", "Amy Zed")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id := books[0].ID

	if err := db.SetCover(id, "42.jpg"); err != nil {
		t.Fatal(err)
	}

	got, err := db.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.CoverPath != "42.jpg" || !got.HasCover {
		t.Errorf("got CoverPath=%q HasCover=%v, want CoverPath=42.jpg HasCover=true", got.CoverPath, got.HasCover)
	}
}
