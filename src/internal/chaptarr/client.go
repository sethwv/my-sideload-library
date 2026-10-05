// Package chaptarr is a minimal read-only client for a self-hosted Chaptarr
// instance (https://github.com/Chaptarr/chaptarr, a Readarr fork for
// audiobook/ebook libraries). Chaptarr has no independently documented REST
// API; the shapes decoded here (GET /api/v1/book, /api/v1/author,
// /api/v1/bookfile, all X-Api-Key-authenticated) were verified directly
// against a live Chaptarr instance during development — see bookDocument,
// authorDocument, and bookFileDocument's doc comments for what's actually
// confirmed vs. inferred.
package chaptarr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultCacheTTL is how long a crawled catalog snapshot (see
// ListBooksCached) is trusted before a background enrichment pass re-fetches
// it — a full crawl is one /api/v1/author call, one /api/v1/book call, and
// one /api/v1/bookfile call per distinct author with files (confirmed live
// against a real instance: 482 authors, ~484 total requests), so this
// trades a bounded amount of staleness for avoiding that cost on every
// pass. The scheduled refresh task keeps the snapshot current for the
// enrichment queue without repeatedly crawling Chaptarr during enrichment.
const DefaultCacheTTL = 12 * time.Hour

const maxResponseBytes = 10 << 20
const maxErrorResponseBytes = 4 << 10
const catalogPageSize = 1000

// Client is safe for concurrent use. baseURL/apiKey/enabled are mutable
// (see SetConfig) so the admin Integrations page can turn Chaptarr on/off
// or change its connection details without a server restart, the same
// shape as internal/hardcover.Client. cachedBooks/cachedAt back
// ListBooksCached and may be persisted through CacheStore.
type Client struct {
	http *http.Client

	mu          sync.Mutex
	baseURL     string
	apiKey      string
	enabled     bool
	cachedBooks []Book
	cachedAt    time.Time
	cacheStore  CacheStore
}

// New creates a Client using the given base URL (e.g.
// "http://chaptarr.local:8978", no trailing slash required), API key, and
// enabled state, as loaded from users.IntegrationSettings at startup. Use
// SetConfig to update any of these at runtime.
func New(enabled bool, baseURL, apiKey string) *Client {
	return NewWithTransport(enabled, baseURL, apiKey, nil)
}

// NewWithTransport creates a Client with transport. A nil transport uses the
// default HTTP transport. The production timeout remains 30 seconds.
func NewWithTransport(enabled bool, baseURL, apiKey string, transport http.RoundTripper) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		enabled: enabled,
		http:    &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

// SetCacheStore enables durable catalog caching and restores the current
// configuration's most recent successful snapshot.
func (c *Client) SetCacheStore(store CacheStore) error {
	c.mu.Lock()
	c.cacheStore = store
	scope := c.cacheScopeLocked()
	c.mu.Unlock()
	if store == nil || scope == "" {
		return nil
	}
	books, refreshedAt, err := store.LoadChaptarrCache(scope)
	if err != nil {
		return err
	}
	if refreshedAt.IsZero() {
		return nil
	}
	c.mu.Lock()
	c.cachedBooks, c.cachedAt = books, refreshedAt
	c.mu.Unlock()
	return nil
}

// SetConfig updates the client's enabled state, base URL, and API key in
// place — called after the admin Integrations page saves a change. Also drops
// any cached catalog snapshot when the endpoint changes.
func (c *Client) SetConfig(enabled bool, baseURL, apiKey string) {
	c.mu.Lock()
	oldScope := c.cacheScopeLocked()
	c.enabled = enabled
	c.baseURL = strings.TrimRight(baseURL, "/")
	c.apiKey = apiKey
	c.cachedBooks = nil
	c.cachedAt = time.Time{}
	store := c.cacheStore
	newScope := c.cacheScopeLocked()
	c.mu.Unlock()
	if store != nil && oldScope != "" && oldScope != newScope {
		if err := store.ClearChaptarrCache(oldScope); err != nil {
			// Cache invalidation is best-effort here. A mismatched scope can
			// never be loaded by the new configuration.
			return
		}
	}
	if store != nil && newScope != "" {
		books, refreshedAt, err := store.LoadChaptarrCache(newScope)
		if err == nil && !refreshedAt.IsZero() {
			c.mu.Lock()
			c.cachedBooks, c.cachedAt = books, refreshedAt
			c.mu.Unlock()
		}
	}
}

// Enabled reports whether Chaptarr is turned on and has both a base URL and
// API key configured — callers should skip matching entirely (not error)
// when this is false, since the integration is optional.
func (c *Client) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled && c.baseURL != "" && c.apiKey != ""
}

func (c *Client) config() (baseURL, apiKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.baseURL, c.apiKey
}

func (c *Client) cacheScopeLocked() string {
	if c.baseURL == "" || c.apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(c.baseURL))
	return fmt.Sprintf("%x", sum[:])
}

// CachedBooks returns the most recently successful snapshot and when it was
// refreshed. It never makes network requests.
func (c *Client) CachedBooks() ([]Book, time.Time) {
	if c == nil {
		return nil, time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cachedBooks, c.cachedAt
}

// NextRefreshAt reports when the current snapshot must be refreshed. A zero
// time means no successful snapshot exists yet.
func (c *Client) NextRefreshAt() time.Time {
	_, refreshedAt := c.CachedBooks()
	if refreshedAt.IsZero() {
		return time.Time{}
	}
	return refreshedAt.Add(DefaultCacheTTL)
}

// Book is a single title Chaptarr is tracking with an on-disk file, with
// just the fields this app needs for path-based matching and metadata
// fill-in. Only books Chaptarr reports as having a file are ever returned
// by ListBooks — one with no file can't be path-matched anyway.
type Book struct {
	ID          int
	Title       string
	Authors     []string
	Series      string
	SeriesIndex float64
	Genres      []string
	Rating      float64 // Chaptarr's ratings.value, e.g. sourced from Hardcover/Goodreads
	// HardcoverID is the numeric Hardcover book ID (suitable for
	// hardcover.Client.GetByID), parsed from hardcoverBookId — empty if
	// Chaptarr sourced this book from somewhere other than Hardcover.
	HardcoverID string
	// Paths are every on-disk file path Chaptarr reports for this book
	// (one per format/edition it's tracking).
	Paths []string
}

// bookDocument mirrors the fields GET /api/v1/book actually returns, as
// observed against a live instance. Notably: no per-book file path (see
// bookFileDocument, a separate endpoint) and no non-empty "overview"
// description in practice (the field exists but was empty on every book
// checked, likely populated only when Chaptarr has separately fetched
// jacket copy) — Book has no Description field as a result; Genres is the
// richer signal this integration actually contributes.
type bookDocument struct {
	ID              int      `json:"id"`
	Title           string   `json:"title"`
	AuthorID        int      `json:"authorId"`
	SeriesTitle     string   `json:"seriesTitle"` // e.g. "Mistborn #1" — see parseSeriesTitle
	Genres          []string `json:"genres"`
	HasFiles        bool     `json:"hasFiles"`
	HardcoverBookID string   `json:"hardcoverBookId"` // e.g. "hc:1686204" when Chaptarr sourced this book from Hardcover; see parseHardcoverID
	Ratings         struct {
		Value float64 `json:"value"`
	} `json:"ratings"`
}

// authorDocument mirrors GET /api/v1/author's per-author fields.
type authorDocument struct {
	ID         int    `json:"id"`
	AuthorName string `json:"authorName"`
}

// bookFileDocument mirrors GET /api/v1/bookfile's per-file fields. This
// endpoint requires an authorId, bookId, or bookFileIds filter — it 400s
// with no filter at all — so ListBooks calls it once per author that has
// at least one book with files, not once globally.
type bookFileDocument struct {
	BookID int    `json:"bookId"`
	Path   string `json:"path"`
}

// seriesTitleRE splits Chaptarr's combined "<series name> #<index>" field
// (confirmed format, including fractional indices like "#6.5" for
// novellas/interludes) back into name and index.
var seriesTitleRE = regexp.MustCompile(`^(.*)\s+#([0-9]+(?:\.[0-9]+)?)$`)

// parseHardcoverID strips Chaptarr's "hc:" source prefix from
// hardcoverBookId, returning "" if the book wasn't sourced from Hardcover
// (Chaptarr also sources books from Goodreads-only, e.g. "gr:123334051-ebook",
// which isn't a Hardcover-queryable ID) or the field is blank.
func parseHardcoverID(hardcoverBookID string) string {
	const prefix = "hc:"
	s := strings.TrimSpace(hardcoverBookID)
	if len(s) <= len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return ""
	}
	return s[len(prefix):]
}

func parseSeriesTitle(s string) (name string, index float64) {
	m := seriesTitleRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return strings.TrimSpace(s), 0
	}
	var idx float64
	fmt.Sscanf(m[2], "%f", &idx)
	return strings.TrimSpace(m[1]), idx
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	baseURL, apiKey := c.config()
	if baseURL == "" {
		return fmt.Errorf("chaptarr: no base URL configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("chaptarr: build request: %w", err)
	}
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("chaptarr: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		message, err := readErrorResponse(resp.Body)
		if err != nil {
			return fmt.Errorf("chaptarr: read error response for %s: %w", path, err)
		}
		if message != "" {
			return fmt.Errorf("chaptarr: unexpected status %d for %s: %s", resp.StatusCode, path, message)
		}
		return fmt.Errorf("chaptarr: unexpected status %d for %s", resp.StatusCode, path)
	}
	data, err := readResponse(resp.Body, maxResponseBytes)
	if err != nil {
		return fmt.Errorf("chaptarr: decode response for %s: %w", path, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("chaptarr: decode response for %s: %w", path, err)
	}
	return nil
}

func readResponse(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func readErrorResponse(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxErrorResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxErrorResponseBytes {
		data = data[:maxErrorResponseBytes]
	}
	return strings.Join(strings.Fields(string(data)), " "), nil
}

// ListBooks fetches every book Chaptarr has an on-disk file for, with
// author names resolved and file paths attached. This is a handful of
// requests, not one per book: one for the full book list, one for the full
// author list (build once, id->name lookup), then one GET /api/v1/bookfile
// call per distinct author that has at least one book with files (that
// endpoint requires an authorId/bookId filter, so it can't be fetched in a
// single global call).
func (c *Client) ListBooks(ctx context.Context) ([]Book, error) {
	var authorDocs []authorDocument
	if err := c.get(ctx, "/api/v1/author", &authorDocs); err != nil {
		return nil, err
	}
	authorNames := make(map[int]string, len(authorDocs))
	for _, a := range authorDocs {
		authorNames[a.ID] = a.AuthorName
	}

	bookDocs, err := c.listBookPages(ctx)
	if err != nil {
		return nil, err
	}

	withFiles := bookDocs[:0:0]
	authorIDs := map[int]bool{}
	for _, d := range bookDocs {
		if !d.HasFiles {
			continue
		}
		withFiles = append(withFiles, d)
		authorIDs[d.AuthorID] = true
	}

	pathsByBookID := map[int][]string{}
	for authorID := range authorIDs {
		var files []bookFileDocument
		if err := c.get(ctx, fmt.Sprintf("/api/v1/bookfile?authorId=%d", authorID), &files); err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Path != "" {
				pathsByBookID[f.BookID] = append(pathsByBookID[f.BookID], f.Path)
			}
		}
	}

	books := make([]Book, 0, len(withFiles))
	for _, d := range withFiles {
		paths := pathsByBookID[d.ID]
		if len(paths) == 0 {
			// hasFiles said yes but the per-author bookfile fetch didn't
			// turn up a path for this specific book — nothing to match on.
			continue
		}
		series, seriesIndex := parseSeriesTitle(d.SeriesTitle)
		b := Book{
			ID:          d.ID,
			Title:       d.Title,
			Series:      series,
			SeriesIndex: seriesIndex,
			Genres:      d.Genres,
			Rating:      d.Ratings.Value,
			HardcoverID: parseHardcoverID(d.HardcoverBookID),
			Paths:       paths,
		}
		if name := authorNames[d.AuthorID]; name != "" {
			b.Authors = []string{name}
		}
		books = append(books, b)
	}
	return books, nil
}

type pagedBookDocument struct {
	Records    []bookDocument `json:"records"`
	TotalCount int            `json:"totalCount"`
}

// listBookPages uses Chaptarr's paged endpoint because /api/v1/book returns
// the entire library in a single unbounded response.
func (c *Client) listBookPages(ctx context.Context) ([]bookDocument, error) {
	var books []bookDocument
	for offset := 0; ; {
		var page pagedBookDocument
		path := fmt.Sprintf("/api/v1/book/paged?offset=%d&pageSize=%d", offset, catalogPageSize)
		if err := c.get(ctx, path, &page); err != nil {
			return nil, err
		}
		books = append(books, page.Records...)
		offset += len(page.Records)
		if len(page.Records) == 0 || offset >= page.TotalCount {
			return books, nil
		}
	}
}

// ListBooksCached returns the cached catalog snapshot if it's younger than
// ttl, or calls RefreshBooks (a full crawl via ListBooks) and caches the
// result otherwise. The returned bool reports whether the cache was used
// (true) or a fresh crawl just happened (false).
func (c *Client) ListBooksCached(ctx context.Context, ttl time.Duration) ([]Book, bool, error) {
	c.mu.Lock()
	if c.cachedBooks != nil && time.Since(c.cachedAt) < ttl {
		cached := c.cachedBooks
		c.mu.Unlock()
		return cached, true, nil
	}
	c.mu.Unlock()

	books, err := c.RefreshBooks(ctx)
	return books, false, err
}

// RefreshBooks force-crawls Chaptarr's catalog via ListBooks, bypassing any
// cached snapshot, and updates the cache for future ListBooksCached calls.
func (c *Client) RefreshBooks(ctx context.Context) ([]Book, error) {
	books, err := c.ListBooks(ctx)
	if err != nil {
		return nil, err
	}
	refreshedAt := time.Now()
	c.mu.Lock()
	store := c.cacheStore
	scope := c.cacheScopeLocked()
	c.mu.Unlock()
	if store != nil && scope != "" {
		if err := store.SaveChaptarrCache(scope, books, refreshedAt); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	c.cachedBooks, c.cachedAt = books, refreshedAt
	c.mu.Unlock()
	return books, nil
}
