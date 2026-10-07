package hardcover

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBestConfidentMatch_AcceptsMatchingAuthor(t *testing.T) {
	matches := []Match{{Title: "Mistborn", Authors: []string{"Brandon Sanderson"}}}
	got, ok := BestConfidentMatch(matches, "Mistborn", "Brandon Sanderson")
	if !ok {
		t.Fatal("expected a confident match")
	}
	if got.Title != "Mistborn" {
		t.Errorf("Title = %q, want %q", got.Title, "Mistborn")
	}
}

func TestBestConfidentMatch_AcceptsCaseInsensitiveSubstring(t *testing.T) {
	matches := []Match{{Title: "Mistborn", Authors: []string{"brandon sanderson"}}}
	if _, ok := BestConfidentMatch(matches, "Mistborn", "Brandon Sanderson"); !ok {
		t.Error("expected case-insensitive author match to be confident")
	}
}

func TestBestConfidentMatch_RejectsMismatchedAuthor(t *testing.T) {
	matches := []Match{{Title: "Mistborn", Authors: []string{"Someone Else"}}}
	if _, ok := BestConfidentMatch(matches, "Mistborn", "Brandon Sanderson"); ok {
		t.Error("expected a mismatched author to not be confident")
	}
}

func TestBestConfidentMatch_NoResults(t *testing.T) {
	if _, ok := BestConfidentMatch(nil, "Mistborn", "Brandon Sanderson"); ok {
		t.Error("expected no results to not be confident")
	}
}

func TestBestConfidentMatch_AcceptsTopResultWhenAuthorUnknown(t *testing.T) {
	matches := []Match{{Title: "Mistborn", Authors: []string{"Anyone"}}}
	if _, ok := BestConfidentMatch(matches, "Mistborn", ""); !ok {
		t.Error("expected top result to be accepted when we don't know the author ourselves")
	}
}

func TestBestConfidentMatch_SkipsBundleInFavorOfStandaloneEdition(t *testing.T) {
	matches := []Match{
		{Title: "The Mistborn Trilogy Boxed Set", Authors: []string{"Brandon Sanderson"}},
		{Title: "Mistborn", Authors: []string{"Brandon Sanderson"}},
	}
	got, ok := BestConfidentMatch(matches, "Mistborn", "Brandon Sanderson")
	if !ok {
		t.Fatal("expected a confident match")
	}
	if got.Title != "Mistborn" {
		t.Errorf("Title = %q, want the standalone edition to be preferred over the box set", got.Title)
	}
}

func TestBestConfidentMatch_RejectsBundleEvenAsOnlyAuthorPlausibleCandidate(t *testing.T) {
	matches := []Match{
		{Title: "Someone Else's Book", Authors: []string{"Someone Else"}},
		{Title: "The Mistborn Omnibus", Authors: []string{"Brandon Sanderson"}},
	}
	if _, ok := BestConfidentMatch(matches, "Mistborn", "Brandon Sanderson"); ok {
		t.Error("expected a bundle to be rejected even as the only author-plausible candidate — auto-fill must never overwrite a title with an omnibus's name")
	}
}

func TestBestConfidentMatch_RejectsMultiBookAudiobookBundle(t *testing.T) {
	// Regression case found live: this bundle's title shares enough tokens
	// with "A Game of Thrones" to pass the title-overlap check, and it's
	// the real author, so only the bundle-keyword check catches it.
	matches := []Match{
		{
			Title:   "George R. R. Martin Song of Ice and Fire Audiobook Bundle: A Game of Thrones (HBO Tie-in), A Clash of Kings (HBO Tie-in), A Storm of Swords, A Feast for Crows, and A Dance with Dragons",
			Authors: []string{"George R. R. Martin"},
		},
	}
	if _, ok := BestConfidentMatch(matches, "A Game of Thrones", "George R. R. Martin"); ok {
		t.Error("expected a multi-book bundle to be rejected, not auto-applied as the book's new title")
	}
}

func TestBestConfidentMatch_FuzzyAuthorTokenOverlap(t *testing.T) {
	matches := []Match{{Title: "Mistborn", Authors: []string{"Sanderson, Brandon"}}}
	if _, ok := BestConfidentMatch(matches, "Mistborn", "Brandon Sanderson"); !ok {
		t.Error("expected token-overlap fallback to match a reordered author name")
	}
}

func TestBestConfidentMatch_RejectsSpinoffCalendar(t *testing.T) {
	matches := []Match{
		{
			Title:   "Quotes from George R. R. Martin's A Game of Thrones Book Series 2016 Day-to-Day Calendar",
			Authors: []string{"George R. R. Martin"},
		},
	}
	if _, ok := BestConfidentMatch(matches, "A Game of Thrones", "George R. R. Martin"); ok {
		t.Error("expected a same-author tie-in calendar to be rejected, not treated as a confident match")
	}
}

func TestBestConfidentMatch_RejectsSpinoffCompanionGuideEvenWithoutBundleOrOtherCandidates(t *testing.T) {
	matches := []Match{
		{Title: "The Unofficial Study Guide to A Game of Thrones", Authors: []string{"George R. R. Martin"}},
	}
	if _, ok := BestConfidentMatch(matches, "A Game of Thrones", "George R. R. Martin"); ok {
		t.Error("expected a study guide to be rejected even as the only author-plausible candidate")
	}
}

func TestBestConfidentMatch_RejectsUnrelatedTitleFromSameAuthor(t *testing.T) {
	matches := []Match{
		{Title: "Fire & Blood", Authors: []string{"George R. R. Martin"}},
	}
	if _, ok := BestConfidentMatch(matches, "A Game of Thrones", "George R. R. Martin"); ok {
		t.Error("expected a different book by the same author to be rejected as a title mismatch")
	}
}

func TestBestConfidentMatch_AcceptsCloseSubtitleVariant(t *testing.T) {
	matches := []Match{
		{Title: "A Game of Thrones: A Song of Ice and Fire, Book One", Authors: []string{"George R. R. Martin"}},
	}
	got, ok := BestConfidentMatch(matches, "A Game of Thrones", "George R. R. Martin")
	if !ok {
		t.Fatal("expected an edition with extra subtitle text to still match")
	}
	if got.Title != "A Game of Thrones: A Song of Ice and Fire, Book One" {
		t.Errorf("Title = %q, unexpected candidate returned", got.Title)
	}
}

func TestBestConfidentMatch_BlankKnownTitleSkipsTitleCheck(t *testing.T) {
	matches := []Match{{Title: "Anything At All", Authors: []string{"Brandon Sanderson"}}}
	if _, ok := BestConfidentMatch(matches, "", "Brandon Sanderson"); !ok {
		t.Error("expected a blank known title to skip the title-similarity check entirely")
	}
}

// TestGetByID_ParsesRealFieldShapes uses a stub response modeled directly
// on a real books_by_pk query verified live against a Hardcover account
// (see search.go's getByIDQuery doc comment): no flat "genres" field
// (comes from cached_tags.Genre), ISBNs under default_physical_edition,
// series under book_series (not featured_series like the search document).
func TestGetByID_ParsesRealFieldShapes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"books_by_pk":{
			"title": "Adversary to the Villain",
			"description": "A hilarious fantasy romance.",
			"pages": 444,
			"rating": 3.79,
			"release_date": "2026-08-03",
			"cached_tags": {"Genre": [{"tag": "Fantasy"}, {"tag": "Fiction"}], "Tag": [], "Mood": [], "Content Warning": []},
			"image": {"url": "https://assets.hardcover.app/cover.jpg"},
			"default_physical_edition": {
				"isbn_10": "1682816729",
				"isbn_13": "9781682816721",
				"publisher": {"name": "Entangled: Red Tower Books"}
			},
			"book_series": [{"position": 4, "series": {"name": "Assistant to the Villain"}}]
		}}}`))
	}))
	defer srv.Close()

	c := New(true, "test-token")
	c.http = srv.Client()
	overrideEndpointForTest(t, srv.URL)

	m, d, err := c.GetByID(context.Background(), "1686204")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Adversary to the Villain" || m.Pages != 444 || m.Rating != 3.79 {
		t.Errorf("Match = %+v, unexpected title/pages/rating", m)
	}
	if m.Series != "Assistant to the Villain" || m.SeriesIndex != 4 {
		t.Errorf("Match = %+v, unexpected series fields", m)
	}
	if len(m.Genres) != 2 || m.Genres[0] != "Fantasy" || m.Genres[1] != "Fiction" {
		t.Errorf("Genres = %v, want [Fantasy Fiction] from cached_tags.Genre", m.Genres)
	}
	if len(m.ISBNs) != 2 || m.ISBNs[0] != "9781682816721" || m.ISBNs[1] != "1682816729" {
		t.Errorf("ISBNs = %v, want [9781682816721 1682816729] (ISBN-13 first)", m.ISBNs)
	}
	if d.Publisher != "Entangled: Red Tower Books" {
		t.Errorf("Detail.Publisher = %q, unexpected", d.Publisher)
	}
	if d.Image != "https://assets.hardcover.app/cover.jpg" {
		t.Errorf("Detail.Image = %q, unexpected", d.Image)
	}
}

func TestGetByID_MissingBook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"books_by_pk":null}}`))
	}))
	defer srv.Close()

	c := New(true, "test-token")
	c.http = srv.Client()
	overrideEndpointForTest(t, srv.URL)

	m, d, err := c.GetByID(context.Background(), "999")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "" || d.Publisher != "" {
		t.Errorf("expected a zero Match/Detail for a missing book, got m=%+v d=%+v", m, d)
	}
}

func TestGetByIDs_BatchesKnownIDs(t *testing.T) {
	var variables struct {
		IDs []int64 `json:"ids"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string          `json:"query"`
			Variables json.RawMessage `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.Query, "BooksByIDs") {
			t.Errorf("query = %q, want batch query", request.Query)
		}
		if err := json.Unmarshal(request.Variables, &variables); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{"data":{"books":[{"id":42,"title":"Mistborn","image":{"url":"https://covers.example/mistborn.jpg"},"default_physical_edition":{"publisher":{"name":"Tor"}}}]}}`))
	}))
	defer srv.Close()

	c := New(true, "test-token")
	c.http = srv.Client()
	overrideEndpointForTest(t, srv.URL)
	matches, details, err := c.GetByIDs(context.Background(), []string{"42", "42", "99"})
	if err != nil {
		t.Fatal(err)
	}
	if len(variables.IDs) != 2 || variables.IDs[0] != 42 || variables.IDs[1] != 99 {
		t.Errorf("ids = %v, want deduplicated [42 99]", variables.IDs)
	}
	if matches["42"].Title != "Mistborn" || details["42"].Publisher != "Tor" {
		t.Errorf("batch result = %+v %+v", matches["42"], details["42"])
	}
	if _, ok := matches["99"]; ok {
		t.Error("missing Hardcover ID should be omitted")
	}
}
