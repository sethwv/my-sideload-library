package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterExpiresAndClearsEntries(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter()
	limiter.now = func() time.Time { return now }

	if !limiter.allowed("login", "reader", 2, time.Minute) {
		t.Fatal("new limiter rejected attempt")
	}
	limiter.record("login", "reader", time.Minute)
	limiter.record("login", "reader", time.Minute)
	if limiter.allowed("login", "reader", 2, time.Minute) {
		t.Fatal("limiter allowed exhausted key")
	}
	limiter.clear("login", "reader")
	if !limiter.allowed("login", "reader", 2, time.Minute) {
		t.Fatal("cleared key remained limited")
	}

	limiter.record("login", "reader", time.Minute)
	now = now.Add(time.Minute)
	if !limiter.allowed("login", "reader", 1, time.Minute) {
		t.Fatal("expired entry remained limited")
	}
}

func TestRequestIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	if got := requestIP(req); got != "192.0.2.1" {
		t.Errorf("requestIP() = %q", got)
	}
	req.RemoteAddr = "malformed"
	if got := requestIP(req); got != "malformed" {
		t.Errorf("requestIP() malformed = %q", got)
	}
}
