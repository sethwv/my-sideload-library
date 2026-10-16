package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAccessLogsSafeRequestFields(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter(&out, slog.LevelDebug)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /books/{id}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	req := httptest.NewRequest(http.MethodGet, "/books/42?token=secret&sort=title", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	rec := httptest.NewRecorder()
	HTTPAccess(logger, mux).ServeHTTP(rec, req)
	got := out.String()
	for _, forbidden := range []string{"secret", "Authorization"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("unsafe access log field %q in %s", forbidden, got)
		}
	}
	for _, want := range []string{" HTTP 200 GET /books/42?sort=title&token=[REDACTED]", "bytes=2", "duration=", "xhr=true"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}

func TestHTTPAccessMarksHTMXRequests(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter(&out, slog.LevelDebug)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /tasks", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/tasks", nil)
	req.Header.Set("HX-Request", "true")
	HTTPAccess(logger, mux).ServeHTTP(httptest.NewRecorder(), req)
	if got := out.String(); !strings.Contains(got, "xhr=true") {
		t.Fatalf("HTMX request was not marked: %s", got)
	}
}

func TestHTTPFailuresLogsServerErrorsWithoutAccessLogging(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter(&out, slog.LevelInfo)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /broken", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "failure", http.StatusInternalServerError) })
	req := httptest.NewRequest(http.MethodGet, "/broken?token=secret", nil)
	HTTPFailures(logger, mux).ServeHTTP(httptest.NewRecorder(), req)
	got := out.String()
	if !strings.Contains(got, "http request failed") || !strings.Contains(got, "status=500") {
		t.Fatalf("missing failure record: %s", got)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "/broken?") {
		t.Fatalf("unsafe failure record: %s", got)
	}
}

func TestHTTPAccessSkipsStaticAssets(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter(&out, slog.LevelDebug)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("asset")) })
	HTTPAccess(logger, mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/static/site.css", nil))
	if got := out.String(); got != "" {
		t.Fatalf("static request was logged: %s", got)
	}
}

func TestHTTPAccessSkipsSuccessfulCovers(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter(&out, slog.LevelDebug)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /covers/{id}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("cover")) })
	HTTPAccess(logger, mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/covers/42", nil))
	if got := out.String(); got != "" {
		t.Fatalf("successful cover request was logged: %s", got)
	}
}
