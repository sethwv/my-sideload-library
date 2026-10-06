package web

import (
	"net"
	"testing"

	"github.com/sethwv/my-sideload-library/internal/chaptarr"
	"github.com/sethwv/my-sideload-library/internal/hardcover"
)

func TestConnectionHardcoverMatchPrefersExactISBN(t *testing.T) {
	matches := []hardcover.Match{
		{ID: "wrong", Title: "Similar Title", Authors: []string{"Other Author"}, ISBNs: []string{"9780000000000"}},
		{ID: "right", Title: "Corrected Title", Authors: []string{"Correct Author"}, ISBNs: []string{"978-1-2345-6789-7"}},
	}
	match, ok := connectionHardcoverMatch(matches, "9781234567897", "Different Export Title", "Different Export Author")
	if !ok || match.ID != "right" {
		t.Fatalf("connectionHardcoverMatch() = %+v, %t; want exact ISBN match", match, ok)
	}
}

func TestResolveCoverURLPinsPublicAddress(t *testing.T) {
	originalLookupIP := lookupIP
	t.Cleanup(func() { lookupIP = originalLookupIP })
	lookupIP = func(host string) ([]net.IP, error) {
		if host != "covers.example" {
			t.Fatalf("resolved unexpected host %q", host)
		}
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	}

	u, requestHost, serverName, err := resolveCoverURL("https://covers.example:8443/book.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.Host, "203.0.113.10:8443"; got != want {
		t.Errorf("dial host = %q, want %q", got, want)
	}
	if requestHost != "covers.example:8443" {
		t.Errorf("request host = %q", requestHost)
	}
	if serverName != "covers.example" {
		t.Errorf("TLS server name = %q", serverName)
	}
}

func TestResolveCoverURLPreservesPathAndQuery(t *testing.T) {
	originalLookupIP := lookupIP
	t.Cleanup(func() { lookupIP = originalLookupIP })
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	}

	u, _, _, err := resolveCoverURL("https://covers.example/images/a%20cover.jpg?size=large")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.EscapedPath(), "/images/a%20cover.jpg"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := u.RawQuery, "size=large"; got != want {
		t.Errorf("query = %q, want %q", got, want)
	}
}

func TestResolveCoverURLRejectsUnsafeTargets(t *testing.T) {
	originalLookupIP := lookupIP
	t.Cleanup(func() { lookupIP = originalLookupIP })
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}

	for _, rawURL := range []string{
		"http://covers.example/book.jpg",
		"https://user@covers.example/book.jpg",
		"https://covers.example/book.jpg",
	} {
		if _, _, _, err := resolveCoverURL(rawURL); err == nil {
			t.Errorf("resolveCoverURL(%q) succeeded", rawURL)
		}
	}
}

func TestMergeChaptarrFields_ChaptarrWinsWhenBothHaveAField(t *testing.T) {
	match := chaptarr.Book{
		Title:       "Onyx Storm",
		Series:      "The Empyrean",
		SeriesIndex: 3,
		Genres:      []string{"Fantasy"},
		Rating:      4.5,
	}
	hc := hardcover.Match{
		Title:       "Onyx Storm (Hardcover title)",
		Series:      "Empyrean (Hardcover series)",
		SeriesIndex: 99,
		Genres:      []string{"Romance"},
		Rating:      1.0,
	}
	f := mergeChaptarrFields(match, hc, hardcover.Detail{})
	if f.Title != "Onyx Storm" || f.Series != "The Empyrean" || f.SeriesIndex != 3 || f.Rating != 4.5 {
		t.Errorf("f = %+v, want Chaptarr's values to win where it has one", f)
	}
	if len(f.Genres) != 1 || f.Genres[0] != "Fantasy" {
		t.Errorf("Genres = %v, want Chaptarr's [Fantasy] to win", f.Genres)
	}
}

func TestMergeChaptarrFields_HardcoverFillsWhatChaptarrLacks(t *testing.T) {
	match := chaptarr.Book{Title: "Onyx Storm"}
	hc := hardcover.Match{
		Title:       "Onyx Storm",
		Description: "A dragon rider saga.",
		ReleaseDate: "2026-01-20",
		Pages:       512,
		ISBNs:       []string{"9781234567897", "1234567891"},
		Rating:      4.5,
	}
	hcDetail := hardcover.Detail{Publisher: "Entangled: Red Tower Books"}
	f := mergeChaptarrFields(match, hc, hcDetail)
	if f.Description != "A dragon rider saga." {
		t.Errorf("Description = %q, want Hardcover's (Chaptarr never has one)", f.Description)
	}
	if f.Publisher != "Entangled: Red Tower Books" {
		t.Errorf("Publisher = %q, want Hardcover's", f.Publisher)
	}
	if f.PublishedDate != "2026-01-20" {
		t.Errorf("PublishedDate = %q, want Hardcover's", f.PublishedDate)
	}
	if f.Pages != 512 {
		t.Errorf("Pages = %d, want 512 from Hardcover", f.Pages)
	}
	if f.ISBN != "9781234567897" {
		t.Errorf("ISBN = %q, want the ISBN-13 preferred by firstISBN", f.ISBN)
	}
	if f.Rating != 4.5 {
		t.Errorf("Rating = %v, want Hardcover's since Chaptarr had none", f.Rating)
	}
}

func TestMergeChaptarrFields_ChaptarrOnlyWhenHardcoverDisabledOrNoMatch(t *testing.T) {
	match := chaptarr.Book{
		Title:       "Onyx Storm",
		Series:      "The Empyrean",
		SeriesIndex: 3,
		Genres:      []string{"Fantasy"},
		Rating:      4.5,
	}
	// Zero-value hc/hcDetail — the shape callers pass when Hardcover is
	// disabled, the Chaptarr book has no HardcoverID, or the lookup failed.
	f := mergeChaptarrFields(match, hardcover.Match{}, hardcover.Detail{})
	if f.Title != "Onyx Storm" || f.Series != "The Empyrean" || f.SeriesIndex != 3 || f.Rating != 4.5 {
		t.Errorf("f = %+v, want Chaptarr's own fields preserved", f)
	}
	if f.Description != "" || f.Publisher != "" || f.Pages != 0 || f.ISBN != "" {
		t.Errorf("f = %+v, want every Hardcover-only field left blank with no daisy-chained lookup", f)
	}
}
