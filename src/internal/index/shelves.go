package index

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	ShelfVisibilityPrivate = "private"
	ShelfVisibilityShared  = "shared"
	ShelfVisibilityPublic  = "public"

	ShelfKindManual      = "manual"
	ShelfKindIntegration = "integration"
)

// Shelf is a named, per-user collection of books. "Favourites" is the first
// system-provided shelf; user-created shelves (with their own management UI)
// are a natural future extension of this same table.
type Shelf struct {
	ID         int64
	Username   string
	Slug       string
	Name       string
	IsSystem   bool
	Visibility string
	Kind       string
	Provider   string
	RemoteKey  string
}

func (s Shelf) IsIntegration() bool { return s.Kind == ShelfKindIntegration }

// ShelfAccess includes the current user's access role for a visible shelf.
// Role is owner, member, or reader (for a public shelf).
type ShelfAccess struct {
	Shelf
	Role string
}

type shelfScanner interface {
	Scan(...any) error
}

func scanShelf(scanner shelfScanner, shelf *Shelf) error {
	var isSystem int
	err := scanner.Scan(&shelf.ID, &shelf.Username, &shelf.Slug, &shelf.Name, &isSystem, &shelf.Visibility, &shelf.Kind, &shelf.Provider, &shelf.RemoteKey)
	shelf.IsSystem = isSystem != 0
	return err
}

func scanShelfAccess(scanner shelfScanner, shelf *ShelfAccess) error {
	var isSystem int
	err := scanner.Scan(&shelf.ID, &shelf.Username, &shelf.Slug, &shelf.Name, &isSystem, &shelf.Visibility, &shelf.Kind, &shelf.Provider, &shelf.RemoteKey, &shelf.Role)
	shelf.IsSystem = isSystem != 0
	return err
}

// EnsureSystemShelf returns the id of the given system shelf for username,
// creating it (marked is_system) if it doesn't exist yet.
func (d *DB) EnsureSystemShelf(username, slug, name string) (int64, error) {
	var id int64
	err := d.sql.QueryRow(`SELECT id FROM shelves WHERE username = ? AND slug = ?`, username, slug).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	res, err := d.sql.Exec(
		`INSERT INTO shelves (username, slug, name, is_system, visibility, kind, created_at) VALUES (?, ?, ?, 1, 'private', 'manual', ?)`,
		username, slug, name, time.Now().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("create shelf: %w", err)
	}
	return res.LastInsertId()
}

// ListShelves returns every shelf owned by username, system shelves first.
func (d *DB) ListShelves(username string) ([]Shelf, error) {
	rows, err := d.sql.Query(
		`SELECT id, username, slug, name, is_system, visibility, kind, integration_provider, integration_shelf_key FROM shelves WHERE username = ? ORDER BY is_system DESC, name`,
		username,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shelves []Shelf
	for rows.Next() {
		var sh Shelf
		if err := scanShelf(rows, &sh); err != nil {
			return nil, err
		}
		shelves = append(shelves, sh)
	}
	return shelves, rows.Err()
}

// ListVisibleShelves returns shelves the user may browse. Owners, explicit
// members, and authenticated readers of public shelves are included.
func (d *DB) ListVisibleShelves(username string) ([]ShelfAccess, error) {
	rows, err := d.sql.Query(`SELECT s.id, s.username, s.slug, s.name, s.is_system, s.visibility, s.kind, s.integration_provider, s.integration_shelf_key,
		CASE WHEN s.username = ? THEN 'owner' WHEN sm.username IS NOT NULL THEN 'member' ELSE 'reader' END
		FROM shelves s LEFT JOIN shelf_members sm ON sm.shelf_id = s.id AND sm.username = ?
		WHERE s.username = ? OR (s.kind = 'manual' AND (sm.username IS NOT NULL OR s.visibility = 'public'))
		ORDER BY CASE WHEN s.username = ? THEN 0 WHEN s.visibility = 'shared' THEN 1 ELSE 2 END,
		s.is_system DESC, s.name COLLATE NOCASE`, username, username, username, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shelves []ShelfAccess
	for rows.Next() {
		var shelf ShelfAccess
		if err := scanShelfAccess(rows, &shelf); err != nil {
			return nil, err
		}
		shelves = append(shelves, shelf)
	}
	return shelves, rows.Err()
}

// ListEditableShelves returns owned shelves plus explicitly shared shelves.
// It is used by book controls, where public read access must not create a
// mutation target.
func (d *DB) ListEditableShelves(username string) ([]Shelf, error) {
	rows, err := d.sql.Query(`SELECT s.id, s.username, s.slug, s.name, s.is_system, s.visibility, s.kind, s.integration_provider, s.integration_shelf_key
		FROM shelves s LEFT JOIN shelf_members sm ON sm.shelf_id = s.id AND sm.username = ?
		WHERE (s.username = ? OR sm.username IS NOT NULL) AND s.kind = 'manual'
		ORDER BY s.is_system DESC, s.name COLLATE NOCASE`, username, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shelves []Shelf
	for rows.Next() {
		var shelf Shelf
		if err := scanShelf(rows, &shelf); err != nil {
			return nil, err
		}
		shelves = append(shelves, shelf)
	}
	return shelves, rows.Err()
}

// ListUserShelves returns every non-system shelf for administrator management.
func (d *DB) ListUserShelves() ([]Shelf, error) {
	rows, err := d.sql.Query(`SELECT id, username, slug, name, is_system, visibility, kind, integration_provider, integration_shelf_key FROM shelves WHERE is_system = 0 ORDER BY username COLLATE NOCASE, name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shelves []Shelf
	for rows.Next() {
		var shelf Shelf
		if err := scanShelf(rows, &shelf); err != nil {
			return nil, err
		}
		shelves = append(shelves, shelf)
	}
	return shelves, rows.Err()
}

// GetShelf returns the shelf with the given id, or nil if it doesn't exist.
// Callers must check Shelf.Username against the current user before acting
// on it — a shelf id alone doesn't prove ownership.
func (d *DB) GetShelf(id int64) (*Shelf, error) {
	var sh Shelf
	err := scanShelf(d.sql.QueryRow(
		`SELECT id, username, slug, name, is_system, visibility, kind, integration_provider, integration_shelf_key FROM shelves WHERE id = ?`, id,
	), &sh)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sh, nil
}

// GetOwnedShelf returns a shelf owned by username, or nil when it does not
// exist. It centralizes the owner scope required by private shelves.
func (d *DB) GetOwnedShelf(username string, id int64) (*Shelf, error) {
	var sh Shelf
	err := scanShelf(d.sql.QueryRow(
		`SELECT id, username, slug, name, is_system, visibility, kind, integration_provider, integration_shelf_key FROM shelves WHERE id = ? AND username = ?`, id, username,
	), &sh)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sh, nil
}

// GetVisibleShelf returns nil for an inaccessible shelf so callers do not
// disclose private shelf metadata.
func (d *DB) GetVisibleShelf(username string, id int64) (*ShelfAccess, error) {
	var shelf ShelfAccess
	err := scanShelfAccess(d.sql.QueryRow(`SELECT s.id, s.username, s.slug, s.name, s.is_system, s.visibility, s.kind, s.integration_provider, s.integration_shelf_key,
		CASE WHEN s.username = ? THEN 'owner' WHEN sm.username IS NOT NULL THEN 'member' ELSE 'reader' END
		FROM shelves s LEFT JOIN shelf_members sm ON sm.shelf_id = s.id AND sm.username = ?
		WHERE s.id = ? AND (s.username = ? OR (s.kind = 'manual' AND (sm.username IS NOT NULL OR s.visibility = 'public')))`,
		username, username, id, username,
	), &shelf)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &shelf, nil
}

// GetEditableShelf returns a shelf the user owns or has explicit membership
// for. Public visibility never grants write access.
func (d *DB) GetEditableShelf(username string, id int64) (*ShelfAccess, error) {
	shelf, err := d.GetVisibleShelf(username, id)
	if err != nil || shelf == nil || shelf.IsIntegration() || (shelf.Role != "owner" && shelf.Role != "member") {
		return nil, err
	}
	return shelf, nil
}

// CreateShelf creates a private non-system shelf for username. A non-positive
// limit allows unlimited shelves for administrative callers.
func (d *DB) CreateShelf(username, name string, limit int) (*Shelf, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("shelf name is required")
	}
	if len(name) > 100 {
		return nil, fmt.Errorf("shelf name must be 100 characters or fewer")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM shelves WHERE username = ? AND is_system = 0 AND kind = 'manual'`, username).Scan(&count); err != nil {
		return nil, err
	}
	if limit > 0 && count >= limit {
		return nil, fmt.Errorf("you can create at most %d shelves", limit)
	}
	var exists int
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM shelves WHERE username = ? AND is_system = 0 AND kind = 'manual' AND name = ? COLLATE NOCASE)`, username, name).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 0 {
		return nil, fmt.Errorf("a shelf with that name already exists")
	}
	slug, err := shelfSlug(tx, username, name)
	if err != nil {
		return nil, err
	}
	result, err := tx.Exec(`INSERT INTO shelves (username, slug, name, visibility, kind, created_at) VALUES (?, ?, ?, 'private', 'manual', ?)`, username, slug, name, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("create shelf: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Shelf{ID: id, Username: username, Slug: slug, Name: name, Visibility: "private", Kind: ShelfKindManual}, nil
}

// EnsureIntegrationShelf returns the private shelf managed by a provider. The
// provider and remote key form its durable external identity, not its display
// name or generated slug.
func (d *DB) EnsureIntegrationShelf(username, provider, remoteKey, name string) (int64, error) {
	var id int64
	err := d.withTx(func(tx *sql.Tx) error {
		var err error
		id, err = ensureIntegrationShelf(tx, username, provider, remoteKey, name)
		return err
	})
	return id, err
}

func ensureIntegrationShelf(tx *sql.Tx, username, provider, remoteKey, name string) (int64, error) {
	username, provider, remoteKey, name = strings.TrimSpace(username), strings.TrimSpace(provider), strings.TrimSpace(remoteKey), strings.TrimSpace(name)
	if username == "" || provider == "" || remoteKey == "" || name == "" {
		return 0, fmt.Errorf("integration shelf identity and name are required")
	}
	if len(name) > 100 {
		return 0, fmt.Errorf("shelf name must be 100 characters or fewer")
	}
	var id int64
	err := tx.QueryRow(`SELECT id FROM shelves WHERE username = ? AND kind = 'integration' AND integration_provider = ? AND integration_shelf_key = ?`, username, provider, remoteKey).Scan(&id)
	if err == nil {
		_, err = tx.Exec(`UPDATE shelves SET name = ?, visibility = 'private' WHERE id = ?`, name, id)
		return id, err
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	slug, err := shelfSlug(tx, username, provider+"-"+remoteKey)
	if err != nil {
		return 0, err
	}
	result, err := tx.Exec(`INSERT INTO shelves (username, slug, name, visibility, kind, integration_provider, integration_shelf_key, created_at) VALUES (?, ?, ?, 'private', 'integration', ?, ?, ?)`, username, slug, name, provider, remoteKey, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("create integration shelf: %w", err)
	}
	return result.LastInsertId()
}

// ReplaceIntegrationShelfBooks makes an integration shelf's local-book
// membership exactly match a successful provider snapshot.
func (d *DB) ReplaceIntegrationShelfBooks(username, provider, remoteKey, name string, bookIDs []int64) (int64, error) {
	var shelfID int64
	err := d.withTx(func(tx *sql.Tx) error {
		var err error
		shelfID, err = ensureIntegrationShelf(tx, username, provider, remoteKey, name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM shelf_books WHERE shelf_id = ?`, shelfID); err != nil {
			return err
		}
		seen := make(map[int64]struct{}, len(bookIDs))
		for _, bookID := range bookIDs {
			if bookID <= 0 {
				continue
			}
			if _, ok := seen[bookID]; ok {
				continue
			}
			seen[bookID] = struct{}{}
			if _, err := tx.Exec(`INSERT INTO shelf_books (shelf_id, book_id, added_at) VALUES (?, ?, ?)`, shelfID, bookID, time.Now().Unix()); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE shelves SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), shelfID)
		return err
	})
	return shelfID, err
}

// DeleteIntegrationShelf removes a provider-managed shelf after a successful
// deselection, disconnect, or source-shelf removal.
func (d *DB) DeleteIntegrationShelf(username, provider, remoteKey string) error {
	return d.withTx(func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRow(`SELECT id FROM shelves WHERE username = ? AND kind = 'integration' AND integration_provider = ? AND integration_shelf_key = ?`, username, provider, remoteKey).Scan(&id)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM shelf_books WHERE shelf_id = ?`, id); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM shelves WHERE id = ?`, id)
		return err
	})
}

func (d *DB) RenameShelf(username string, id int64, name string) error {
	return d.renameShelf(id, username, name)
}

func (d *DB) RenameShelfForManager(id int64, name string) error {
	return d.renameShelf(id, "", name)
}

func (d *DB) renameShelf(id int64, username, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("shelf name is required")
	}
	if len(name) > 100 {
		return fmt.Errorf("shelf name must be 100 characters or fewer")
	}
	query := `UPDATE shelves SET name = ? WHERE id = ? AND is_system = 0 AND kind = 'manual'`
	args := []any{name, id}
	if username != "" {
		query += ` AND username = ?`
		args = append(args, username)
	}
	result, err := d.sql.Exec(query, args...)
	if err != nil {
		if strings.Contains(err.Error(), "idx_shelves_owner_name") || strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("a shelf with that name already exists")
		}
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("shelf not found")
	}
	return nil
}

func (d *DB) DeleteShelf(username string, id int64) error {
	return d.deleteShelf(id, username)
}

// DeleteShelfForManager deletes any non-system shelf. Callers must check the
// administrator-level manage_shelves permission before using it.
func (d *DB) DeleteShelfForManager(id int64) error {
	return d.deleteShelf(id, "")
}

func (d *DB) deleteShelf(id int64, username string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `DELETE FROM shelves WHERE id = ? AND is_system = 0 AND kind = 'manual'`
	args := []any{id}
	if username != "" {
		query += ` AND username = ?`
		args = append(args, username)
	}
	result, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("shelf not found")
	}
	if _, err := tx.Exec(`DELETE FROM shelf_books WHERE shelf_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM shelf_members WHERE shelf_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) SetShelfVisibility(username string, id int64, visibility string) error {
	return d.setShelfVisibility(id, username, visibility)
}

func (d *DB) SetShelfVisibilityForManager(id int64, visibility string) error {
	return d.setShelfVisibility(id, "", visibility)
}

func (d *DB) setShelfVisibility(id int64, username, visibility string) error {
	if visibility != ShelfVisibilityPrivate && visibility != ShelfVisibilityShared && visibility != ShelfVisibilityPublic {
		return fmt.Errorf("invalid shelf visibility")
	}
	query := `UPDATE shelves SET visibility = ? WHERE id = ? AND is_system = 0 AND kind = 'manual'`
	args := []any{visibility, id}
	if username != "" {
		query += ` AND username = ?`
		args = append(args, username)
	}
	result, err := d.sql.Exec(query, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("shelf not found")
	}
	return nil
}

func (d *DB) ListShelfMembers(owner string, id int64) ([]ShelfAccess, error) {
	shelf, err := d.GetOwnedShelf(owner, id)
	if err != nil || shelf == nil || shelf.IsSystem || shelf.IsIntegration() {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("shelf not found")
	}
	rows, err := d.sql.Query(`SELECT username FROM shelf_members WHERE shelf_id = ? ORDER BY username COLLATE NOCASE`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []ShelfAccess
	for rows.Next() {
		var member ShelfAccess
		if err := rows.Scan(&member.Username); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (d *DB) ListShelfMembersForManager(id int64) ([]ShelfAccess, error) {
	rows, err := d.sql.Query(`SELECT username FROM shelf_members WHERE shelf_id = ? ORDER BY username COLLATE NOCASE`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []ShelfAccess
	for rows.Next() {
		var member ShelfAccess
		if err := rows.Scan(&member.Username); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (d *DB) AddShelfMember(owner string, id int64, member string) error {
	return d.addShelfMember(id, owner, member)
}

func (d *DB) AddShelfMemberForManager(id int64, member string) error {
	return d.addShelfMember(id, "", member)
}

func (d *DB) addShelfMember(id int64, owner, member string) error {
	if strings.TrimSpace(member) == "" {
		return fmt.Errorf("invalid shelf member")
	}
	var shelf *Shelf
	var err error
	if owner == "" {
		shelf, err = d.GetShelf(id)
	} else {
		shelf, err = d.GetOwnedShelf(owner, id)
	}
	if err != nil || shelf == nil || shelf.IsSystem || shelf.IsIntegration() {
		if err != nil {
			return err
		}
		return fmt.Errorf("shelf not found")
	}
	if member == shelf.Username {
		return fmt.Errorf("invalid shelf member")
	}
	if shelf.Visibility == ShelfVisibilityPrivate {
		return fmt.Errorf("private shelves cannot have members")
	}
	_, err = d.sql.Exec(`INSERT OR IGNORE INTO shelf_members (shelf_id, username, created_at) VALUES (?, ?, ?)`, id, member, time.Now().Unix())
	return err
}

func (d *DB) RemoveShelfMember(owner string, id int64, member string) error {
	return d.removeShelfMember(id, owner, member)
}

func (d *DB) RemoveShelfMemberForManager(id int64, member string) error {
	return d.removeShelfMember(id, "", member)
}

func (d *DB) removeShelfMember(id int64, owner, member string) error {
	var shelf *Shelf
	var err error
	if owner == "" {
		shelf, err = d.GetShelf(id)
	} else {
		shelf, err = d.GetOwnedShelf(owner, id)
	}
	if err != nil || shelf == nil || shelf.IsSystem || shelf.IsIntegration() {
		if err != nil {
			return err
		}
		return fmt.Errorf("shelf not found")
	}
	if shelf.Visibility == ShelfVisibilityPrivate {
		return fmt.Errorf("private shelves cannot have members")
	}
	result, err := d.sql.Exec(`DELETE FROM shelf_members WHERE shelf_id = ? AND username = ?`, id, member)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("shelf member not found")
	}
	return nil
}

// DeleteUserShelves removes all shelves owned by username and any memberships
// that grant username access to someone else's shelf.
func (d *DB) DeleteUserShelves(username string) error {
	return d.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM shelf_members WHERE username = ?`, username); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM shelf_members WHERE shelf_id IN (SELECT id FROM shelves WHERE username = ?)`, username); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM shelf_books WHERE shelf_id IN (SELECT id FROM shelves WHERE username = ?)`, username); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM shelves WHERE username = ?`, username)
		return err
	})
}

// RecentShelf prefers the most recently added writable non-system shelf
// containing bookID and otherwise returns the user's most recently used
// writable non-system shelf.
func (d *DB) RecentShelf(username string, bookID int64) (*Shelf, error) {
	var sh Shelf
	err := scanShelf(d.sql.QueryRow(`SELECT s.id, s.username, s.slug, s.name, s.is_system, s.visibility, s.kind, s.integration_provider, s.integration_shelf_key
		FROM shelves s JOIN shelf_books sb ON sb.shelf_id = s.id
		LEFT JOIN shelf_members sm ON sm.shelf_id = s.id AND sm.username = ?
		WHERE (s.username = ? OR sm.username IS NOT NULL) AND s.is_system = 0 AND s.kind = 'manual' AND sb.book_id = ?
		ORDER BY sb.added_at DESC, s.id DESC LIMIT 1`, username, username, bookID,
	), &sh)
	if err == sql.ErrNoRows {
		err = scanShelf(d.sql.QueryRow(`SELECT s.id, s.username, s.slug, s.name, s.is_system, s.visibility, s.kind, s.integration_provider, s.integration_shelf_key
			FROM shelves s LEFT JOIN shelf_members sm ON sm.shelf_id = s.id AND sm.username = ?
			WHERE (s.username = ? OR sm.username IS NOT NULL) AND s.is_system = 0 AND s.kind = 'manual' AND s.last_used_at > 0
			ORDER BY s.last_used_at DESC, s.id DESC LIMIT 1`, username, username,
		), &sh)
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sh, nil
}

type shelfSlugQueryer interface {
	QueryRow(string, ...any) *sql.Row
}

func shelfSlug(q shelfSlugQueryer, username, name string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "shelf"
	}
	for n := 1; ; n++ {
		slug := base
		if n > 1 {
			slug += "-" + strconv.Itoa(n)
		}
		var exists bool
		if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM shelves WHERE username = ? AND slug = ?)`, username, slug).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
	}
}

// IsBookOnShelf reports whether bookID is already on shelfID.
func (d *DB) IsBookOnShelf(shelfID, bookID int64) (bool, error) {
	var exists int
	err := d.sql.QueryRow(`SELECT 1 FROM shelf_books WHERE shelf_id = ? AND book_id = ?`, shelfID, bookID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// AddBookToShelf adds bookID to shelfID, a no-op if already present.
func (d *DB) AddBookToShelf(shelfID, bookID int64) error {
	var kind string
	if err := d.sql.QueryRow(`SELECT kind FROM shelves WHERE id = ?`, shelfID).Scan(&kind); err != nil {
		return err
	}
	if kind == ShelfKindIntegration {
		return fmt.Errorf("integration shelf membership is managed by its provider")
	}
	_, err := d.sql.Exec(
		`INSERT OR IGNORE INTO shelf_books (shelf_id, book_id, added_at) VALUES (?, ?, ?)`,
		shelfID, bookID, time.Now().Unix(),
	)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`UPDATE shelves SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), shelfID)
	return err
}

// RemoveBookFromShelf removes bookID from shelfID, a no-op if not present.
func (d *DB) RemoveBookFromShelf(shelfID, bookID int64) error {
	var kind string
	if err := d.sql.QueryRow(`SELECT kind FROM shelves WHERE id = ?`, shelfID).Scan(&kind); err != nil {
		return err
	}
	if kind == ShelfKindIntegration {
		return fmt.Errorf("integration shelf membership is managed by its provider")
	}
	_, err := d.sql.Exec(`DELETE FROM shelf_books WHERE shelf_id = ? AND book_id = ?`, shelfID, bookID)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`UPDATE shelves SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), shelfID)
	return err
}

// ShelfBookIDs returns the set of book ids on shelfID, for marking state
// (e.g. a filled star) across a whole listing page in one query.
func (d *DB) ShelfBookIDs(shelfID int64) (map[int64]bool, error) {
	rows, err := d.sql.Query(`SELECT book_id FROM shelf_books WHERE shelf_id = ?`, shelfID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// ShelfMemberships returns the given user's shelf membership for displayed
// books. Joining shelves scopes the result to the owner without adding one
// query parameter per shelf.
func (d *DB) ShelfMemberships(username string, bookIDs []int64) (map[int64]map[int64]bool, error) {
	memberships := make(map[int64]map[int64]bool)
	if len(bookIDs) == 0 {
		return memberships, nil
	}
	args := make([]any, 0, len(bookIDs)+2)
	args = append(args, username, username)
	for _, bookID := range bookIDs {
		args = append(args, bookID)
	}
	placeholders := func(n int) string {
		return strings.TrimRight(strings.Repeat("?,", n), ",")
	}
	rows, err := d.sql.Query(
		`SELECT shelf_books.shelf_id, shelf_books.book_id FROM shelf_books JOIN shelves ON shelves.id = shelf_books.shelf_id
		LEFT JOIN shelf_members ON shelf_members.shelf_id = shelves.id AND shelf_members.username = ?
		WHERE (shelves.username = ? OR shelf_members.username IS NOT NULL) AND shelf_books.book_id IN (`+placeholders(len(bookIDs))+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var shelfID, bookID int64
		if err := rows.Scan(&shelfID, &bookID); err != nil {
			return nil, err
		}
		if memberships[shelfID] == nil {
			memberships[shelfID] = make(map[int64]bool)
		}
		memberships[shelfID][bookID] = true
	}
	return memberships, rows.Err()
}
