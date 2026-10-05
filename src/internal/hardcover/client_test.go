package hardcover

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestNewWithTransport_UsesInjectedTransport(t *testing.T) {
	var gotMethod, gotAuth, gotContentType, gotUserAgent string
	c := NewWithTransport(true, "hc_pat_test", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotUserAgent = r.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":{"search":{"ids":[],"results":{"hits":[]}}}}`)),
			Header:     make(http.Header),
		}, nil
	}))

	if _, err := c.Search(context.Background(), "Mistborn", "", ""); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotAuth != "Bearer hc_pat_test" || gotContentType != "application/json" || gotUserAgent != userAgent {
		t.Errorf("request = method %q, authorization %q, content type %q, user agent %q", gotMethod, gotAuth, gotContentType, gotUserAgent)
	}
	if c.http.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", c.http.Timeout)
	}
}

func TestSearch_RejectsOversizedResponse(t *testing.T) {
	c := NewWithTransport(true, "test-token", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), maxResponseBytes+1))),
			Header:     make(http.Header),
		}, nil
	}))

	if _, err := c.Search(context.Background(), "Mistborn", "", ""); err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Errorf("Search error = %v, want oversized response error", err)
	}
}

// overrideEndpointForTest points the package-level endpoint at a test
// server for the duration of t, restoring the real URL afterward.
func overrideEndpointForTest(t *testing.T, url string) {
	t.Helper()
	original := endpoint
	endpoint = url
	t.Cleanup(func() { endpoint = original })
}

func TestSearch_SendsPersonalAPIKeyAndVariables(t *testing.T) {
	var gotAuth, gotUserAgent string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUserAgent = r.Header.Get("User-Agent")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"data":{"search":{"ids":[1],"results":{"hits":[{"document":{"id":"1","title":"Mistborn","author_names":["Brandon Sanderson"],"release_date":"2006-01-01","featured_series":{"position":1,"series":{"name":"The Mistborn Saga"}}}}]}}}}`))
	}))
	defer srv.Close()

	c := New(true, "hc_pat_test")
	c.http = srv.Client()
	overrideEndpointForTest(t, srv.URL)

	matches, err := c.Search(context.Background(), "Mistborn", "Brandon Sanderson", "")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer hc_pat_test" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer hc_pat_test")
	}
	if gotUserAgent != userAgent {
		t.Errorf("User-Agent = %q, want %q", gotUserAgent, userAgent)
	}
	variables, _ := gotBody["variables"].(map[string]any)
	if variables["q"] != "Mistborn Brandon Sanderson" {
		t.Errorf("variables[q] = %v, want %q", variables["q"], "Mistborn Brandon Sanderson")
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1", len(matches))
	}
	m := matches[0]
	if m.Title != "Mistborn" || m.Series != "The Mistborn Saga" || m.SeriesIndex != 1 || m.ReleaseDate != "2006-01-01" {
		t.Errorf("match = %+v, unexpected fields", m)
	}
}

func TestSearch_SurfacesGraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"message":"rate limited"}]}`))
	}))
	defer srv.Close()

	c := New(true, "test-token")
	c.http = srv.Client()
	overrideEndpointForTest(t, srv.URL)

	if _, err := c.Search(context.Background(), "Mistborn", "Brandon Sanderson", ""); err == nil {
		t.Fatal("expected an error from a GraphQL errors response")
	} else if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v, want it to mention %q", err, "rate limited")
	}
}

func TestClient_Enabled(t *testing.T) {
	if (&Client{}).Enabled() {
		t.Error("expected a client with no token to be disabled")
	}
	if !New(true, "a-token").Enabled() {
		t.Error("expected an enabled client with a token to be enabled")
	}
	if New(false, "a-token").Enabled() {
		t.Error("expected a client with a token but enabled=false to be disabled")
	}
	var nilClient *Client
	if nilClient.Enabled() {
		t.Error("expected a nil client to be disabled")
	}
}

func TestClient_SetConfigTakesEffectImmediately(t *testing.T) {
	c := New(false, "")
	if c.Enabled() {
		t.Fatal("expected a freshly-created disabled client to be disabled")
	}
	c.SetConfig(true, "new-token")
	if !c.Enabled() {
		t.Error("expected SetConfig(true, ...) to enable the client")
	}
	c.SetConfig(false, "new-token")
	if c.Enabled() {
		t.Error("expected SetConfig(false, ...) to disable the client even with a token set")
	}
}

func TestDetail_ParsesPublisherAndImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"books_by_pk":{"image":{"url":"https://covers.example/mistborn.jpg"},"default_physical_edition":{"publisher":{"name":"Tor Books"}}}}}`))
	}))
	defer srv.Close()

	c := New(true, "test-token")
	c.http = srv.Client()
	overrideEndpointForTest(t, srv.URL)

	d, err := c.Detail(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if d.Publisher != "Tor Books" {
		t.Errorf("Publisher = %q, want %q", d.Publisher, "Tor Books")
	}
	if d.Image != "https://covers.example/mistborn.jpg" {
		t.Errorf("Image = %q, want %q", d.Image, "https://covers.example/mistborn.jpg")
	}
}

func TestDetail_MissingBook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"books_by_pk":null}}`))
	}))
	defer srv.Close()

	c := New(true, "test-token")
	c.http = srv.Client()
	overrideEndpointForTest(t, srv.URL)

	d, err := c.Detail(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if d.Publisher != "" || d.Image != "" {
		t.Errorf("Detail = %+v, want zero value for a missing book", d)
	}
}

func TestClient_ThrottlesRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"search":{"ids":[],"results":{"hits":[]}}}}`))
	}))
	defer srv.Close()

	c := New(true, "test-token")
	c.http = srv.Client()
	overrideEndpointForTest(t, srv.URL)

	start := time.Now()
	if _, err := c.Search(context.Background(), "a", "b", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(context.Background(), "c", "d", ""); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < minInterval {
		t.Errorf("two calls took %v, want at least %v (rate-limit spacing)", elapsed, minInterval)
	}
}
