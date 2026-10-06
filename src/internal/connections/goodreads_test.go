package connections

import (
	"strings"
	"testing"
)

func TestParseGoodreadsCSV(t *testing.T) {
	snapshot, err := ParseGoodreadsCSV(strings.NewReader("Book Id,Title,Author,ISBN,ISBN13,Exclusive Shelf,Bookshelves,Date Added\n1,One,Author,\"=\"\"123\"\"\",\"=\"\"978123\"\"\",to-read,fiction,2024/01/02\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Shelves) != 2 || len(snapshot.Items) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Items[0].ISBN != "978123" || snapshot.Items[0].AddedAt == 0 || snapshot.Items[0].ExternalID != "1" {
		t.Fatalf("item = %+v", snapshot.Items[0])
	}
}
