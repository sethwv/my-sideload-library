package logging

import (
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// HTTPAccess wraps next with safe, debug-level request completion logging.
// It intentionally excludes targets, query strings, headers, cookies, client
// addresses, and request or response bodies.
func HTTPAccess(logger *slog.Logger, next http.Handler) http.Handler {
	logger = Or(logger, (*httpAccess)(nil))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		if strings.HasPrefix(requestRoute(r), "/static/") || (status == http.StatusOK && r.Pattern == "GET /covers/{id}") {
			return
		}
		attributes := []any{
			"method", r.Method,
			"route", requestTarget(r),
			"status", status,
			"bytes", recorder.bytes,
			"duration", time.Since(started),
		}
		if isXHR(r) {
			attributes = append(attributes, "xhr", true)
		}
		logger.Log(r.Context(), LevelHTTP, "http request completed", attributes...)
	})
}

func requestRoute(r *http.Request) string {
	return strings.TrimPrefix(r.Pattern, r.Method+" ")
}

func requestTarget(r *http.Request) string {
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	query := redactQuery(r.URL.Query())
	if query == "" {
		return path
	}
	return path + "?" + query
}

func redactQuery(query url.Values) string {
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(query))
	for _, key := range keys {
		values := query[key]
		for _, value := range values {
			if sensitiveKey(key) {
				value = "[REDACTED]"
			} else {
				value = url.QueryEscape(value)
			}
			parts = append(parts, url.QueryEscape(key)+"="+value)
		}
	}
	return strings.Join(parts, "&")
}

func isXHR(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest") || r.Header.Get("HX-Request") == "true"
}

// HTTPFailures records unexpected server responses even when access logging is
// disabled. It uses the same deliberately limited request metadata as
// HTTPAccess and does not expose handler errors, request values, or identity.
func HTTPFailures(logger *slog.Logger, next http.Handler) http.Handler {
	logger = Or(logger, (*httpAccess)(nil))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		if status >= http.StatusInternalServerError {
			logger.Error("http request failed",
				"method", r.Method,
				"route", r.Pattern,
				"status", status,
				"bytes", recorder.bytes,
				"duration", time.Since(started),
			)
		}
	})
}

type httpAccess struct{}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}
