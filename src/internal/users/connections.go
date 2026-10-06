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
	RemoteShelfKey string
	ExternalID     string
	Title          string
	Author         string
	ISBN           string
	AddedAt        int64
	SourcePosition int
	LocalBookID    int64
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
	if _, err := tx.Exec(`DELETE FROM connection_items WHERE user_id = ? AND provider = ?`, userID, provider); err != nil {
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
		if _, err := tx.Exec(`INSERT INTO connection_items (user_id, provider, remote_shelf_key, external_id, title, author, isbn, added_at, source_position, local_book_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, userID, provider, item.RemoteShelfKey, item.ExternalID, item.Title, item.Author, item.ISBN, item.AddedAt, item.SourcePosition, item.LocalBookID); err != nil {
			return err
		}
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
	userID, err := s.connectionUserID(username)
	if err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(`SELECT remote_shelf_key, external_id, title, author, isbn, added_at, source_position, local_book_id FROM connection_items WHERE user_id = ? AND provider = ? AND remote_shelf_key = ? ORDER BY added_at DESC, source_position ASC`, userID, provider, remoteShelfKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ConnectionItem
	for rows.Next() {
		var item ConnectionItem
		if err := rows.Scan(&item.RemoteShelfKey, &item.ExternalID, &item.Title, &item.Author, &item.ISBN, &item.AddedAt, &item.SourcePosition, &item.LocalBookID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
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
