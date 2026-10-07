package index

import (
	"path/filepath"
	"testing"
)

func TestNormalizeConnectionText(t *testing.T) {
	for _, test := range []struct{ value, want string }{
		{"Ender’s Game", "endersgame"},
		{"George R.R. Martin", "georgerrmartin"},
		{"George R. R. Martin", "georgerrmartin"},
		{"Allison  King", "allisonking"},
	} {
		if got := normalizeConnectionText(test.value); got != test.want {
			t.Errorf("normalizeConnectionText(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestFindConnectionBookNormalizesProviderFormatting(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "enders.epub"), "Ender's Game", "Orson Scott Card")
	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	bookID, err := db.FindConnectionBook("goodreads", "", "", "Ender’s Game", "Orson  Scott Card")
	if err != nil {
		t.Fatal(err)
	}
	if bookID == 0 {
		t.Fatal("expected punctuation and whitespace normalized match")
	}
}
