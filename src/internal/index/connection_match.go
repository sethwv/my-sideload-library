package index

import (
	"database/sql"
	"strings"
)

// FindConnectionBook resolves a provider item to a local EPUB-backed book.
// ISBN is preferred; title and author are only an exact, case-insensitive
// fallback so external identity is never inferred from a fuzzy match.
func (d *DB) FindConnectionBook(provider, externalID, isbn, title, author string) (int64, error) {
	var id int64
	if provider == "hardcover" && strings.TrimSpace(externalID) != "" {
		err := d.sql.QueryRow(`SELECT book_id FROM book_enrichment WHERE hardcover_id = ? LIMIT 1`, strings.TrimSpace(externalID)).Scan(&id)
		if err == nil {
			return id, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
	}
	isbn = normalizeConnectionISBN(isbn)
	if isbn != "" {
		err := d.sql.QueryRow(`SELECT b.id FROM books b LEFT JOIN book_enrichment be ON be.book_id = b.id WHERE REPLACE(REPLACE(LOWER(COALESCE(be.isbn, b.identifier, '')), '-', ''), ' ', '') = ? LIMIT 1`, isbn).Scan(&id)
		if err == nil {
			return id, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
	}
	err := d.sql.QueryRow(`SELECT id FROM books WHERE LOWER(title) = LOWER(?) AND LOWER(author) = LOWER(?) LIMIT 1`, strings.TrimSpace(title), strings.TrimSpace(author)).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func normalizeConnectionISBN(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer("-", "", " ", "").Replace(value)
}
