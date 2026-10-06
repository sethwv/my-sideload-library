package connections

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ParseGoodreadsCSV reads Goodreads' Library Export format into a normalized
// snapshot. The caller owns the uploaded stream and can reconcile the result
// only after the user has selected its discovered shelves.
func ParseGoodreadsCSV(r io.Reader) (Snapshot, error) {
	reader := csv.NewReader(r)
	header, err := reader.Read()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read Goodreads CSV header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for i, name := range header {
		columns[strings.TrimSpace(name)] = i
	}
	for _, required := range []string{"Book Id", "Title", "Exclusive Shelf", "Bookshelves"} {
		if _, ok := columns[required]; !ok {
			return Snapshot{}, fmt.Errorf("not a Goodreads library export: missing %q column", required)
		}
	}
	value := func(row []string, name string) string {
		i, ok := columns[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	shelves := map[string]string{}
	var items []Item
	position := 0
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Snapshot{}, fmt.Errorf("read Goodreads CSV: %w", err)
		}
		title := value(row, "Title")
		if title == "" {
			continue
		}
		externalID := value(row, "Book Id")
		if externalID == "" {
			continue
		}
		keys := shelfKeys(value(row, "Exclusive Shelf"), value(row, "Bookshelves"))
		addedAt := goodreadsDate(value(row, "Date Added"))
		isbn := cleanISBN(value(row, "ISBN13"))
		if isbn == "" {
			isbn = cleanISBN(value(row, "ISBN"))
		}
		for _, key := range keys {
			shelves[key] = key
			items = append(items, Item{ShelfKey: key, ExternalID: externalID, Title: title, Author: value(row, "Author"), ISBN: isbn, AddedAt: addedAt, Position: position})
		}
		position++
	}
	if len(items) == 0 {
		return Snapshot{}, fmt.Errorf("no Goodreads books found in CSV")
	}
	result := Snapshot{Items: items}
	for key, name := range shelves {
		result.Shelves = append(result.Shelves, Shelf{Key: key, Name: name})
	}
	return result, nil
}

func shelfKeys(exclusive, bookshelves string) []string {
	seen := map[string]bool{}
	var keys []string
	for _, value := range append([]string{exclusive}, strings.Split(bookshelves, ",")...) {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			keys = append(keys, value)
		}
	}
	return keys
}

func cleanISBN(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, `="`) && strings.HasSuffix(value, `"`) {
		value = value[2 : len(value)-1]
	}
	return strings.TrimSpace(value)
}

func goodreadsDate(value string) int64 {
	for _, format := range []string{"2006/01/02", "2006-01-02", "01/02/2006"} {
		if date, err := time.Parse(format, value); err == nil {
			return date.Unix()
		}
	}
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		return unix
	}
	return 0
}
