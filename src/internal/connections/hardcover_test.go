package connections

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type hardcoverRoundTrip func(*http.Request) (*http.Response, error)

func (f hardcoverRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHardcoverSourceSnapshotNormalizesTokenAndMapsShelves(t *testing.T) {
	var authorization string
	source := NewHardcoverSource(hardcoverRoundTrip(func(r *http.Request) (*http.Response, error) {
		authorization = r.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
          "data": {"me": [{"user_books": [
            {"date_added":"2026-10-15T12:00:00Z", "status_id":1, "book":{"id":123,"title":"Book One","contributions":[{"author":{"name":"Author"}}]},"edition":{"isbn_13":"9781234567890"}},
            {"date_added":"", "status_id":6, "book":{"id":"ignored","title":"Ignored"}}
          ]}]}
        }`))}, nil
	}))

	snapshot, err := source.Snapshot(context.Background(), " personal-api-key ")
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer personal-api-key" {
		t.Errorf("authorization = %q", authorization)
	}
	if len(snapshot.Shelves) != 6 {
		t.Fatalf("shelves = %#v", snapshot.Shelves)
	}
	if len(snapshot.Items) != 2 {
		t.Fatalf("items = %#v", snapshot.Items)
	}
	item := snapshot.Items[0]
	if item.ExternalID != "123" || item.Author != "Author" || item.ISBN != "9781234567890" || item.Position != 0 || item.AddedAt == 0 {
		t.Errorf("item = %#v", item)
	}
	if snapshot.Items[1].ShelfKey != "ignored" {
		t.Errorf("ignored item = %#v", snapshot.Items[1])
	}
}

func TestNormalizeHardcoverTokenAddsBearerForCurrentKeys(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"api-key", "Bearer api-key"},
		{"Bearer legacy-token", "Bearer legacy-token"},
		{"eyJ.header.signature", "Bearer eyJ.header.signature"},
	} {
		if got := NormalizeHardcoverToken(test.input); got != test.want {
			t.Errorf("NormalizeHardcoverToken(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestHardcoverSourceDoesNotExposeTokenInErrors(t *testing.T) {
	source := NewHardcoverSource(hardcoverRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("no"))}, nil
	}))
	_, err := source.Snapshot(context.Background(), "secret-token")
	if err == nil || strings.Contains(err.Error(), "secret-token") || err.Error() != "Hardcover rejected that API token" {
		t.Errorf("error = %v", err)
	}
}

func TestHardcoverSourceReportsMissingScope(t *testing.T) {
	source := NewHardcoverSource(hardcoverRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error_description":"Missing scopes: read:me"}`))}, nil
	}))
	_, err := source.Snapshot(context.Background(), "personal-api-key")
	if err == nil || err.Error() != "Hardcover request failed: Missing scopes: read:me" {
		t.Errorf("error = %v", err)
	}
}

func TestHardcoverSourceReturnsBoundedGraphQLError(t *testing.T) {
	source := NewHardcoverSource(hardcoverRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"errors":[{"message":"Cannot query field \"user_books\""}]}`))}, nil
	}))
	_, err := source.Snapshot(context.Background(), "personal-token")
	if err == nil || err.Error() != `Hardcover could not read this library: Cannot query field "user_books"` || strings.Contains(err.Error(), "personal-token") {
		t.Errorf("error = %v", err)
	}
}
