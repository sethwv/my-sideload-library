package chaptarr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestNewWithTransport_UsesInjectedTransport(t *testing.T) {
	var gotPath, gotKey, gotAccept string
	c := NewWithTransport(true, "http://chaptarr.test", "test-key", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Api-Key")
		gotAccept = r.Header.Get("Accept")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
			Header:     make(http.Header),
		}, nil
	}))

	if _, err := c.ListBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/book" || gotKey != "test-key" || gotAccept != "application/json" {
		t.Errorf("request = path %q, key %q, accept %q", gotPath, gotKey, gotAccept)
	}
	if c.http.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", c.http.Timeout)
	}
}

func TestListBooks_RejectsOversizedResponse(t *testing.T) {
	c := NewWithTransport(true, "http://chaptarr.test", "test-key", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), maxResponseBytes+1))),
			Header:     make(http.Header),
		}, nil
	}))

	if _, err := c.ListBooks(context.Background()); err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Errorf("ListBooks error = %v, want oversized response error", err)
	}
}

func TestListBooks_PreservesStatusError(t *testing.T) {
	c := NewWithTransport(true, "http://chaptarr.test", "test-key", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(strings.NewReader(`{"message":"unavailable"}`)),
			Header:     make(http.Header),
		}, nil
	}))

	if _, err := c.ListBooks(context.Background()); err == nil || !strings.Contains(err.Error(), "unexpected status 502 for /api/v1/author") {
		t.Errorf("ListBooks error = %v, want status error naming the endpoint", err)
	}
}

func TestListBooks_IncludesErrorResponseBody(t *testing.T) {
	c := NewWithTransport(true, "http://chaptarr.test", "test-key", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader("  invalid\nAPI key  ")),
			Header:     make(http.Header),
		}, nil
	}))

	if _, err := c.ListBooks(context.Background()); err == nil || !strings.Contains(err.Error(), "unexpected status 401 for /api/v1/author: invalid API key") {
		t.Errorf("ListBooks error = %v, want endpoint, status, and response body", err)
	}
}

type memoryCacheStore struct {
	books map[string][]Book
	at    map[string]time.Time
	err   error
}

func (s *memoryCacheStore) LoadChaptarrCache(scope string) ([]Book, time.Time, error) {
	if s.err != nil {
		return nil, time.Time{}, s.err
	}
	return s.books[scope], s.at[scope], nil
}

func (s *memoryCacheStore) SaveChaptarrCache(scope string, books []Book, refreshedAt time.Time) error {
	if s.err != nil {
		return s.err
	}
	if s.books == nil {
		s.books = map[string][]Book{}
		s.at = map[string]time.Time{}
	}
	s.books[scope], s.at[scope] = books, refreshedAt
	return nil
}

func (s *memoryCacheStore) ClearChaptarrCache(scope string) error {
	if s.err != nil {
		return s.err
	}
	delete(s.books, scope)
	delete(s.at, scope)
	return nil
}

func TestClient_Enabled(t *testing.T) {
	if (&Client{}).Enabled() {
		t.Error("expected a zero-value client to be disabled")
	}
	if !New(true, "http://localhost:8978", "a-key").Enabled() {
		t.Error("expected an enabled client with URL+key to be enabled")
	}
	if New(true, "", "a-key").Enabled() {
		t.Error("expected a client with no base URL to be disabled")
	}
	if New(true, "http://localhost:8978", "").Enabled() {
		t.Error("expected a client with no API key to be disabled")
	}
	if New(false, "http://localhost:8978", "a-key").Enabled() {
		t.Error("expected enabled=false to be disabled regardless of URL/key")
	}
	var nilClient *Client
	if nilClient.Enabled() {
		t.Error("expected a nil client to be disabled")
	}
}

func TestClient_SetConfigTakesEffectImmediately(t *testing.T) {
	c := New(false, "", "")
	if c.Enabled() {
		t.Fatal("expected a freshly-created disabled client to be disabled")
	}
	c.SetConfig(true, "http://localhost:8978", "a-key")
	if !c.Enabled() {
		t.Error("expected SetConfig to enable the client")
	}
}

// TestListBooks_ against a stub server modeled on a real Chaptarr
// instance's response shapes, verified live during development (see
// client.go's package doc comment): GET /api/v1/author (id->authorName),
// GET /api/v1/book (title/authorId/seriesTitle "Name #Index"/genres/
// ratings.value/hasFiles/id, no path), GET /api/v1/bookfile?authorId=N
// (bookId->path, 400s with no filter at all).
func newStubChaptarrServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/author", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id": 1, "authorName": "Brandon Sanderson"}]`))
	})
	mux.HandleFunc("/api/v1/book", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"id": 1, "title": "Mistborn", "authorId": 1, "seriesTitle": "The Mistborn Saga #1", "genres": ["Fantasy"], "ratings": {"value": 4.5}, "hasFiles": true, "hardcoverBookId": "hc:123456"},
			{"id": 2, "title": "No File Yet", "authorId": 1, "seriesTitle": "The Mistborn Saga #2", "genres": ["Fantasy"], "hasFiles": false}
		]`))
	})
	mux.HandleFunc("/api/v1/bookfile", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("authorId") == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message": "authorId, bookId, bookFileIds or unmapped must be provided"}`))
			return
		}
		w.Write([]byte(`[{"bookId": 1, "path": "/library/Brandon Sanderson/Mistborn/Mistborn.epub"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestListBooks_ParsesSeriesTitleAuthorAndPaths(t *testing.T) {
	srv := newStubChaptarrServer(t)

	c := New(true, srv.URL, "test-key")
	books, err := c.ListBooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Only the hasFiles=true, path-resolved book should be returned — book
	// id 2 has no file yet and can't be path-matched.
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1 (the one with a resolvable file path): %+v", len(books), books)
	}
	b := books[0]
	if b.Title != "Mistborn" || len(b.Authors) != 1 || b.Authors[0] != "Brandon Sanderson" {
		t.Errorf("book = %+v, unexpected title/author", b)
	}
	if b.Series != "The Mistborn Saga" || b.SeriesIndex != 1 {
		t.Errorf("book = %+v, want seriesTitle \"The Mistborn Saga #1\" parsed into name+index", b)
	}
	if b.Rating != 4.5 {
		t.Errorf("Rating = %v, want 4.5", b.Rating)
	}
	if len(b.Paths) != 1 || b.Paths[0] != "/library/Brandon Sanderson/Mistborn/Mistborn.epub" {
		t.Errorf("Paths = %v, unexpected", b.Paths)
	}
	if b.HardcoverID != "123456" {
		t.Errorf("HardcoverID = %q, want %q from hardcoverBookId \"hc:123456\"", b.HardcoverID, "123456")
	}
}

// countingChaptarrServer wraps newStubChaptarrServer's response shapes but
// also counts /api/v1/book requests, as a proxy for "how many full crawls
// actually happened" — used to verify ListBooksCached avoids a crawl on a
// cache hit and RefreshBooks always performs one.
func countingChaptarrServer(t *testing.T) (srv *httptest.Server, bookRequests *int32) {
	t.Helper()
	var count int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/author", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id": 1, "authorName": "Brandon Sanderson"}]`))
	})
	mux.HandleFunc("/api/v1/book", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.Write([]byte(`[{"id": 1, "title": "Mistborn", "authorId": 1, "hasFiles": true}]`))
	})
	mux.HandleFunc("/api/v1/bookfile", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"bookId": 1, "path": "/library/Mistborn.epub"}]`))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &count
}

func TestListBooksCached_ServesFromCacheWithinTTL(t *testing.T) {
	srv, bookRequests := countingChaptarrServer(t)
	c := New(true, srv.URL, "test-key")

	if _, fromCache, err := c.ListBooksCached(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	} else if fromCache {
		t.Error("expected the first call to be a fresh crawl, not a cache hit")
	}
	if got := atomic.LoadInt32(bookRequests); got != 1 {
		t.Fatalf("got %d /api/v1/book requests after first call, want 1", got)
	}

	books, fromCache, err := c.ListBooksCached(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !fromCache {
		t.Error("expected the second call within TTL to be served from cache")
	}
	if len(books) != 1 || books[0].Title != "Mistborn" {
		t.Errorf("books = %+v, want the cached Mistborn entry", books)
	}
	if got := atomic.LoadInt32(bookRequests); got != 1 {
		t.Errorf("got %d /api/v1/book requests after a cache hit, want still 1 (no second crawl)", got)
	}
}

func TestListBooksCached_RecrawlsPastTTL(t *testing.T) {
	srv, bookRequests := countingChaptarrServer(t)
	c := New(true, srv.URL, "test-key")

	if _, _, err := c.ListBooksCached(context.Background(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)

	_, fromCache, err := c.ListBooksCached(context.Background(), time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if fromCache {
		t.Error("expected a call past a near-zero TTL to re-crawl, not serve stale cache")
	}
	if got := atomic.LoadInt32(bookRequests); got != 2 {
		t.Errorf("got %d /api/v1/book requests, want 2 (TTL expired between calls)", got)
	}
}

func TestRefreshBooks_AlwaysBypassesCache(t *testing.T) {
	srv, bookRequests := countingChaptarrServer(t)
	c := New(true, srv.URL, "test-key")

	if _, _, err := c.ListBooksCached(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RefreshBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(bookRequests); got != 2 {
		t.Errorf("got %d /api/v1/book requests, want 2 (RefreshBooks must always crawl, even with a warm cache)", got)
	}

	// The warm-from-refresh cache should now serve the next cached call.
	if _, fromCache, err := c.ListBooksCached(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	} else if !fromCache {
		t.Error("expected RefreshBooks to have updated the cache for the next ListBooksCached call")
	}
	if got := atomic.LoadInt32(bookRequests); got != 2 {
		t.Errorf("got %d /api/v1/book requests after the cache-hit call, want still 2", got)
	}
}

func TestSetConfig_InvalidatesCache(t *testing.T) {
	srv, bookRequests := countingChaptarrServer(t)
	c := New(true, srv.URL, "test-key")

	if _, _, err := c.ListBooksCached(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	c.SetConfig(true, srv.URL, "a-different-key")

	if _, fromCache, err := c.ListBooksCached(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	} else if fromCache {
		t.Error("expected SetConfig to invalidate the cache, forcing a fresh crawl")
	}
	if got := atomic.LoadInt32(bookRequests); got != 2 {
		t.Errorf("got %d /api/v1/book requests, want 2 (SetConfig must drop the old cache)", got)
	}
}

func TestClient_RestoresPersistentCache(t *testing.T) {
	srv, bookRequests := countingChaptarrServer(t)
	store := &memoryCacheStore{}
	first := New(true, srv.URL, "test-key")
	if err := first.SetCacheStore(store); err != nil {
		t.Fatal(err)
	}
	if _, err := first.RefreshBooks(context.Background()); err != nil {
		t.Fatal(err)
	}

	restarted := New(true, srv.URL, "test-key")
	if err := restarted.SetCacheStore(store); err != nil {
		t.Fatal(err)
	}
	books, fromCache, err := restarted.ListBooksCached(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !fromCache || len(books) != 1 {
		t.Fatalf("restored cache = (%+v, fromCache=%t), want one cache hit", books, fromCache)
	}
	if got := atomic.LoadInt32(bookRequests); got != 1 {
		t.Errorf("got %d crawls, want only the initial crawl", got)
	}
}

func TestRefreshBooks_DoesNotReplaceCacheWhenPersistenceFails(t *testing.T) {
	srv, _ := countingChaptarrServer(t)
	store := &memoryCacheStore{}
	c := New(true, srv.URL, "test-key")
	if err := c.SetCacheStore(store); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RefreshBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, beforeAt := c.CachedBooks()
	store.err = fmt.Errorf("disk unavailable")
	if _, err := c.RefreshBooks(context.Background()); err == nil {
		t.Fatal("expected persistence error")
	}
	after, afterAt := c.CachedBooks()
	if len(after) != len(before) || !afterAt.Equal(beforeAt) {
		t.Error("failed refresh replaced the last known-good cache")
	}
}

func TestParseHardcoverID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hc:1686204", "1686204"},
		{"HC:1686204", "1686204"},
		{"gr:123334051-ebook", ""},
		{"", ""},
		{"hc:", ""},
	}
	for _, tt := range tests {
		if got := parseHardcoverID(tt.in); got != tt.want {
			t.Errorf("parseHardcoverID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestListBooks_SendsAPIKeyOnEveryRequest(t *testing.T) {
	var gotKeys []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/author", func(w http.ResponseWriter, r *http.Request) {
		gotKeys = append(gotKeys, r.Header.Get("X-Api-Key"))
		w.Write([]byte(`[{"id": 1, "authorName": "Brandon Sanderson"}]`))
	})
	mux.HandleFunc("/api/v1/book", func(w http.ResponseWriter, r *http.Request) {
		gotKeys = append(gotKeys, r.Header.Get("X-Api-Key"))
		w.Write([]byte(`[{"id": 1, "title": "Mistborn", "authorId": 1, "hasFiles": true}]`))
	})
	mux.HandleFunc("/api/v1/bookfile", func(w http.ResponseWriter, r *http.Request) {
		gotKeys = append(gotKeys, r.Header.Get("X-Api-Key"))
		w.Write([]byte(`[{"bookId": 1, "path": "/library/Mistborn.epub"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(true, srv.URL, "test-key")
	if _, err := c.ListBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(gotKeys) != 3 {
		t.Fatalf("got %d requests, want 3 (author, book, bookfile)", len(gotKeys))
	}
	for _, k := range gotKeys {
		if k != "test-key" {
			t.Errorf("X-Api-Key = %q, want %q on every request", k, "test-key")
		}
	}
}

func TestListBooks_NoBaseURL(t *testing.T) {
	c := New(true, "", "test-key")
	if _, err := c.ListBooks(context.Background()); err == nil {
		t.Error("expected an error with no base URL configured")
	}
}

func TestParseSeriesTitle(t *testing.T) {
	tests := []struct {
		in        string
		wantName  string
		wantIndex float64
	}{
		{"Mistborn #1", "Mistborn", 1},
		{"Blood and Ash #6.5", "Blood and Ash", 6.5},
		{"The Hitchhiker’s Guide to the Galaxy #2", "The Hitchhiker’s Guide to the Galaxy", 2},
		{"No Index Here", "No Index Here", 0},
		{"", "", 0},
	}
	for _, tt := range tests {
		name, index := parseSeriesTitle(tt.in)
		if name != tt.wantName || index != tt.wantIndex {
			t.Errorf("parseSeriesTitle(%q) = (%q, %v), want (%q, %v)", tt.in, name, index, tt.wantName, tt.wantIndex)
		}
	}
}
