package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	loginAttemptLimit  = 5
	loginAttemptWindow = 15 * time.Minute
	resetAttemptLimit  = 3
	resetAttemptWindow = time.Hour
)

type rateLimitEntry struct {
	attempts int
	resetAt  time.Time
}

// RateLimiter tracks public authentication attempts in memory. It is scoped to
// one server process, which is sufficient to prevent repeated abuse against a
// single instance without persisting personal identifiers.
type RateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]rateLimitEntry
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{now: time.Now, entries: make(map[string]rateLimitEntry)}
}

func (l *RateLimiter) allowed(scope, key string, limit int, window time.Duration) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.cleanup(now)
	entry, ok := l.entries[scope+":"+key]
	return !ok || now.After(entry.resetAt) || entry.attempts < limit
}

func (l *RateLimiter) record(scope, key string, window time.Duration) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.cleanup(now)
	entryKey := scope + ":" + key
	entry := l.entries[entryKey]
	if entry.resetAt.IsZero() || !now.Before(entry.resetAt) {
		entry = rateLimitEntry{resetAt: now.Add(window)}
	}
	entry.attempts++
	l.entries[entryKey] = entry
}

func (l *RateLimiter) clear(scope, key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, scope+":"+key)
}

func (l *RateLimiter) cleanup(now time.Time) {
	for key, entry := range l.entries {
		if !now.Before(entry.resetAt) {
			delete(l.entries, key)
		}
	}
}

func requestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}

func rateLimitIdentity(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
