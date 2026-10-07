package users

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type Connection struct {
	Provider      string
	Enabled       bool
	HasSecret     bool
	LastSuccessAt int64
	LastAttemptAt int64
	NextAttemptAt int64
	LastError     string
}

type ConnectionShelf struct {
	RemoteKey string
	Name      string
	Selected  bool
}

type ConnectionItem struct {
	RemoteShelfKey                                                                    string
	ExternalID                                                                        string
	Title                                                                             string
	Author                                                                            string
	ISBN                                                                              string
	AddedAt                                                                           int64
	SourcePosition                                                                    int
	LocalBookID                                                                       int64
	HardcoverID, EnrichedTitle, EnrichedAuthor, CoverURL, CoverPath, EnrichmentStatus string
}

type ConnectionEnrichmentCandidate struct {
	Username, Provider, RemoteShelfKey, ExternalID, Title, Author, ISBN string
	HardcoverID, EnrichedTitle, EnrichedAuthor, CoverURL                string
}

// ConnectionEnrichmentStats summarizes background work for provider shelves.
type ConnectionEnrichmentStats struct {
	Pending        int
	UnmatchedLocal int
	CoverCache     int
}

// ConnectionPromotionCandidates returns unresolved provider items whose
// existing metadata can be matched locally without another API request.
func (s *Store) ConnectionPromotionCandidates(limit int) ([]ConnectionEnrichmentCandidate, error) {
	rows, err := s.sql.Query(`SELECT u.username, ci.provider, ci.remote_shelf_key, ci.external_id, ci.title, ci.author, ci.isbn, ci.hardcover_id, ci.enriched_title, ci.enriched_author FROM connection_items ci JOIN users u ON u.id = ci.user_id WHERE ci.local_book_id = 0 ORDER BY ci.provider, ci.remote_shelf_key, ci.source_position LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []ConnectionEnrichmentCandidate
	for rows.Next() {
		var c ConnectionEnrichmentCandidate
		if err := rows.Scan(&c.Username, &c.Provider, &c.RemoteShelfKey, &c.ExternalID, &c.Title, &c.Author, &c.ISBN, &c.HardcoverID, &c.EnrichedTitle, &c.EnrichedAuthor); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

func (s *Store) connectionUserID(username string) (int64, error) {
	var id int64
	err := s.sql.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("user not found")
	}
	return id, err
}

// SaveConnection stores provider credentials. A blank secret preserves an
// existing secret, so callers never need to render it back to the user.
func (s *Store) SaveConnection(username, provider, secret string, enabled bool) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	userID, err := s.connectionUserID(username)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = s.sql.Exec(`INSERT INTO user_connections (user_id, provider, secret, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, provider) DO UPDATE SET secret = CASE WHEN excluded.secret = '' THEN user_connections.secret ELSE excluded.secret END, enabled = excluded.enabled, updated_at = excluded.updated_at`, userID, provider, strings.TrimSpace(secret), boolToInt(enabled), now, now)
	return err
}

func (s *Store) ListConnections(username string) ([]Connection, error) {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(`SELECT provider, enabled, secret != '', last_success_at, last_attempt_at, next_attempt_at, last_error FROM user_connections WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var connections []Connection
	for rows.Next() {
		var connection Connection
		var enabled, hasSecret int
		if err := rows.Scan(&connection.Provider, &enabled, &hasSecret, &connection.LastSuccessAt, &connection.LastAttemptAt, &connection.NextAttemptAt, &connection.LastError); err != nil {
			return nil, err
		}
		connection.Enabled, connection.HasSecret = enabled != 0, hasSecret != 0
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func (s *Store) ReplaceConnectionSnapshot(username, provider string, shelves []ConnectionShelf, items []ConnectionItem) error {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return err
	}
	tx, err := s.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS current_connection_snapshot (remote_shelf_key TEXT NOT NULL, external_id TEXT NOT NULL, PRIMARY KEY (remote_shelf_key, external_id))`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM current_connection_snapshot`); err != nil {
		return err
	}
	seen := make(map[string]bool, len(shelves))
	for _, shelf := range shelves {
		shelf.RemoteKey, shelf.Name = strings.TrimSpace(shelf.RemoteKey), strings.TrimSpace(shelf.Name)
		if shelf.RemoteKey == "" || shelf.Name == "" {
			return fmt.Errorf("connection shelf identity and name are required")
		}
		seen[shelf.RemoteKey] = true
		if _, err := tx.Exec(`INSERT INTO connection_shelves (user_id, provider, remote_key, name, selected) VALUES (?, ?, ?, ?, 0) ON CONFLICT(user_id, provider, remote_key) DO UPDATE SET name = excluded.name`, userID, provider, shelf.RemoteKey, shelf.Name); err != nil {
			return err
		}
	}
	for _, item := range items {
		if item.RemoteShelfKey == "" || item.ExternalID == "" || strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("connection item identity and title are required")
		}
		if !seen[item.RemoteShelfKey] {
			return fmt.Errorf("connection item references unknown shelf")
		}
		if _, err := tx.Exec(`INSERT INTO current_connection_snapshot (remote_shelf_key, external_id) VALUES (?, ?)`, item.RemoteShelfKey, item.ExternalID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO connection_items (user_id, provider, remote_shelf_key, external_id, title, author, isbn, added_at, source_position, local_book_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, provider, remote_shelf_key, external_id) DO UPDATE SET title = excluded.title, author = excluded.author, isbn = excluded.isbn, added_at = excluded.added_at, source_position = excluded.source_position`, userID, provider, item.RemoteShelfKey, item.ExternalID, item.Title, item.Author, item.ISBN, item.AddedAt, item.SourcePosition, item.LocalBookID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM connection_items WHERE user_id = ? AND provider = ? AND NOT EXISTS (SELECT 1 FROM current_connection_snapshot s WHERE s.remote_shelf_key = connection_items.remote_shelf_key AND s.external_id = connection_items.external_id)`, userID, provider); err != nil {
		return err
	}
	if len(seen) == 0 {
		if _, err := tx.Exec(`DELETE FROM connection_shelves WHERE user_id = ? AND provider = ?`, userID, provider); err != nil {
			return err
		}
	} else {
		keys := make([]string, 0, len(seen))
		args := []any{userID, provider}
		for key := range seen {
			keys = append(keys, key)
			args = append(args, key)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
		if _, err := tx.Exec(`DELETE FROM connection_shelves WHERE user_id = ? AND provider = ? AND remote_key NOT IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ConnectionShelves(username, provider string) ([]ConnectionShelf, error) {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(`SELECT remote_key, name, selected FROM connection_shelves WHERE user_id = ? AND provider = ? ORDER BY name COLLATE NOCASE`, userID, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shelves []ConnectionShelf
	for rows.Next() {
		var shelf ConnectionShelf
		var selected int
		if err := rows.Scan(&shelf.RemoteKey, &shelf.Name, &selected); err != nil {
			return nil, err
		}
		shelf.Selected = selected != 0
		shelves = append(shelves, shelf)
	}
	return shelves, rows.Err()
}

func (s *Store) SetConnectionShelfSelection(username, provider, remoteKey string, selected bool) error {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return err
	}
	result, err := s.sql.Exec(`UPDATE connection_shelves SET selected = ? WHERE user_id = ? AND provider = ? AND remote_key = ?`, boolToInt(selected), userID, provider, remoteKey)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return fmt.Errorf("connection shelf not found")
	}
	return nil
}

func (s *Store) ConnectionItems(username, provider, remoteShelfKey string) ([]ConnectionItem, error) {
	return s.ConnectionItemsPage(username, provider, remoteShelfKey, "", "provider", false, 1, 0)
}

// CountConnectionItems returns the number of provider entries matching search.
func (s *Store) CountConnectionItems(username, provider, remoteShelfKey, search string) (int, error) {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return 0, err
	}
	search = strings.TrimSpace(search)
	var count int
	err = s.sql.QueryRow(`SELECT COUNT(*) FROM connection_items WHERE user_id = ? AND provider = ? AND remote_shelf_key = ? AND (? = '' OR title || ' ' || author || ' ' || enriched_title || ' ' || enriched_author LIKE '%' || ? || '%')`, userID, provider, remoteShelfKey, search, search).Scan(&count)
	return count, err
}

// ConnectionItemsPage returns provider entries in provider order by default.
// pageSize zero returns every matching entry for callers that need a snapshot.
func (s *Store) ConnectionItemsPage(username, provider, remoteShelfKey, search, sort string, descending bool, page, pageSize int) ([]ConnectionItem, error) {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return nil, err
	}
	search = strings.TrimSpace(search)
	if page < 1 {
		page = 1
	}
	order := "source_position ASC, external_id ASC"
	if descending {
		order = "source_position DESC, external_id DESC"
	}
	switch sort {
	case "title":
		order = "COALESCE(NULLIF(enriched_title, ''), title) COLLATE NOCASE"
	case "author":
		order = "COALESCE(NULLIF(enriched_author, ''), author) COLLATE NOCASE"
	case "added":
		order = "added_at"
	}
	if sort != "provider" && descending {
		order += " DESC"
	} else if sort != "provider" {
		order += " ASC"
	}
	query := `SELECT remote_shelf_key, external_id, title, author, isbn, added_at, source_position, local_book_id, hardcover_id, enriched_title, enriched_author, cover_url, cover_path, enrichment_status FROM connection_items WHERE user_id = ? AND provider = ? AND remote_shelf_key = ? AND (? = '' OR title || ' ' || author || ' ' || enriched_title || ' ' || enriched_author LIKE '%' || ? || '%') ORDER BY ` + order
	args := []any{userID, provider, remoteShelfKey, search, search}
	if pageSize > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, pageSize, (page-1)*pageSize)
	}
	rows, err := s.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ConnectionItem
	for rows.Next() {
		var item ConnectionItem
		if err := rows.Scan(&item.RemoteShelfKey, &item.ExternalID, &item.Title, &item.Author, &item.ISBN, &item.AddedAt, &item.SourcePosition, &item.LocalBookID, &item.HardcoverID, &item.EnrichedTitle, &item.EnrichedAuthor, &item.CoverURL, &item.CoverPath, &item.EnrichmentStatus); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetConnectionItemCoverPath(c ConnectionEnrichmentCandidate, coverPath string) error {
	userID, err := s.connectionUserID(c.Username)
	if err != nil {
		return err
	}
	_, err = s.sql.Exec(`UPDATE connection_items SET cover_path = ? WHERE user_id = ? AND provider = ? AND remote_shelf_key = ? AND external_id = ?`, coverPath, userID, c.Provider, c.RemoteShelfKey, c.ExternalID)
	return err
}

func (s *Store) SetConnectionItemCoverCacheFailed(c ConnectionEnrichmentCandidate) error {
	userID, err := s.connectionUserID(c.Username)
	if err != nil {
		return err
	}
	_, err = s.sql.Exec(`UPDATE connection_items SET cover_cache_failed = 1 WHERE user_id = ? AND provider = ? AND remote_shelf_key = ? AND external_id = ?`, userID, c.Provider, c.RemoteShelfKey, c.ExternalID)
	return err
}

func (s *Store) ConnectionCoverPath(username, provider, remoteShelfKey, externalID string) (string, error) {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return "", err
	}
	var coverPath string
	err = s.sql.QueryRow(`SELECT cover_path FROM connection_items WHERE user_id = ? AND provider = ? AND remote_shelf_key = ? AND external_id = ?`, userID, provider, remoteShelfKey, externalID).Scan(&coverPath)
	return coverPath, err
}

func (s *Store) ConnectionEnrichmentCandidates(limit int) ([]ConnectionEnrichmentCandidate, error) {
	rows, err := s.sql.Query(`SELECT u.username, ci.provider, ci.remote_shelf_key, ci.external_id, ci.title, ci.author, ci.isbn FROM connection_items ci JOIN users u ON u.id = ci.user_id WHERE ci.local_book_id = 0 AND (ci.enrichment_status = '' OR (ci.enrichment_status = 'error' AND ci.enrichment_next_retry_at <= strftime('%s','now'))) ORDER BY CASE WHEN ci.enrichment_status = 'error' THEN ci.enrichment_next_retry_at ELSE 0 END, ci.added_at DESC, ci.source_position ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []ConnectionEnrichmentCandidate
	for rows.Next() {
		var c ConnectionEnrichmentCandidate
		if err := rows.Scan(&c.Username, &c.Provider, &c.RemoteShelfKey, &c.ExternalID, &c.Title, &c.Author, &c.ISBN); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// ConnectionCoverCacheCandidates returns enriched ghost covers that have not
// been persisted to the local thumbnail store yet.
func (s *Store) ConnectionCoverCacheCandidates(limit int) ([]ConnectionEnrichmentCandidate, error) {
	rows, err := s.sql.Query(`SELECT u.username, ci.provider, ci.remote_shelf_key, ci.external_id, ci.cover_url FROM connection_items ci JOIN users u ON u.id = ci.user_id WHERE ci.local_book_id = 0 AND ci.cover_url != '' AND ci.cover_path = '' AND ci.cover_cache_failed = 0 ORDER BY ci.provider, ci.remote_shelf_key, ci.source_position LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []ConnectionEnrichmentCandidate
	for rows.Next() {
		var c ConnectionEnrichmentCandidate
		if err := rows.Scan(&c.Username, &c.Provider, &c.RemoteShelfKey, &c.ExternalID, &c.CoverURL); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// ConnectionEnrichmentPending returns unresolved ghost items currently eligible
// for the enrichment queue, including retries whose backoff has elapsed.
func (s *Store) ConnectionEnrichmentPending() (int, error) {
	var count int
	err := s.sql.QueryRow(`SELECT COUNT(*) FROM connection_items WHERE local_book_id = 0 AND (enrichment_status = '' OR (enrichment_status = 'error' AND enrichment_next_retry_at <= strftime('%s','now')))`).Scan(&count)
	return count, err
}

func (s *Store) ConnectionEnrichmentQueueStats() (ConnectionEnrichmentStats, error) {
	var stats ConnectionEnrichmentStats
	err := s.sql.QueryRow(`SELECT
		COUNT(*) FILTER (WHERE local_book_id = 0 AND (enrichment_status = '' OR (enrichment_status = 'error' AND enrichment_next_retry_at <= strftime('%s','now')))),
		COUNT(*) FILTER (WHERE local_book_id = 0),
		COUNT(*) FILTER (WHERE local_book_id = 0 AND cover_url != '' AND cover_path = '' AND cover_cache_failed = 0)
		FROM connection_items`).Scan(&stats.Pending, &stats.UnmatchedLocal, &stats.CoverCache)
	return stats, err
}

func (s *Store) SetConnectionItemEnrichment(c ConnectionEnrichmentCandidate, hardcoverID, title, author, coverURL, status string) error {
	userID, err := s.connectionUserID(c.Username)
	if err != nil {
		return err
	}
	if status == "error" {
		_, err = s.sql.Exec(`UPDATE connection_items SET enrichment_status = 'error', enrichment_retry_count = enrichment_retry_count + 1, enrichment_next_retry_at = strftime('%s','now') + MIN(3600, 60 * (1 << MIN(6, enrichment_retry_count))) WHERE user_id = ? AND provider = ? AND remote_shelf_key = ? AND external_id = ?`, userID, c.Provider, c.RemoteShelfKey, c.ExternalID)
		return err
	}
	_, err = s.sql.Exec(`UPDATE connection_items SET hardcover_id = ?, enriched_title = ?, enriched_author = ?, cover_url = ?, cover_cache_failed = 0, enrichment_status = ?, enrichment_retry_count = 0, enrichment_next_retry_at = 0 WHERE user_id = ? AND provider = ? AND remote_shelf_key = ? AND external_id = ?`, hardcoverID, title, author, coverURL, status, userID, c.Provider, c.RemoteShelfKey, c.ExternalID)
	return err
}

func (s *Store) SetConnectionItemMatch(username, provider, remoteShelfKey, externalID string, localBookID int64) error {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return err
	}
	_, err = s.sql.Exec(`UPDATE connection_items SET local_book_id = ? WHERE user_id = ? AND provider = ? AND remote_shelf_key = ? AND external_id = ?`, localBookID, userID, provider, remoteShelfKey, externalID)
	return err
}

func (s *Store) ConnectionSecret(username, provider string) (string, error) {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return "", err
	}
	var secret string
	err = s.sql.QueryRow(`SELECT secret FROM user_connections WHERE user_id = ? AND provider = ?`, userID, provider).Scan(&secret)
	return secret, err
}

func (s *Store) SetConnectionSyncResult(username, provider string, success bool, nextAttemptAt int64, message string) error {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	if len(message) > 1000 {
		message = message[:1000]
	}
	if success {
		message = ""
	}
	_, err = s.sql.Exec(`UPDATE user_connections SET last_attempt_at = ?, last_success_at = CASE WHEN ? THEN ? ELSE last_success_at END, next_attempt_at = ?, last_error = ?, updated_at = ? WHERE user_id = ? AND provider = ?`, now, boolToInt(success), now, nextAttemptAt, message, now, userID, provider)
	return err
}

func (s *Store) DeleteConnection(username, provider string) error {
	userID, err := s.connectionUserID(username)
	if err != nil {
		return err
	}
	_, err = s.sql.Exec(`DELETE FROM user_connections WHERE user_id = ? AND provider = ?`, userID, provider)
	return err
}
