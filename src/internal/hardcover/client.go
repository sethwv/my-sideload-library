// Package hardcover is a minimal client for the Hardcover GraphQL API
// (https://docs.hardcover.app/api/), used to fill in and clean up book
// metadata (series, release date, title) that a library's EPUB files often
// don't have or get wrong.
package hardcover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// endpoint is variable for legacy package tests. New tests can instead inject
// a transport through NewWithTransport without mutating package state.
var endpoint = "https://api.hardcover.app/v1/graphql"

const maxResponseBytes = 10 << 20

const userAgent = "my-sideload-library"

// Client is safe for concurrent use. Every call goes through a shared rate
// limiter (Hardcover's own limit is 60 requests/min) regardless of which
// goroutine (the background enrichment queue, or a manual per-book check)
// is asking, so nothing needs its own separate throttling logic. token/
// enabled are mutable (see SetConfig) so the admin Integrations page can
// flip Hardcover on/off or rotate its token without a server restart; the
// same mutex that guards the rate limiter's lastCallTime also guards these.
type Client struct {
	http *http.Client

	mu          sync.Mutex
	token       string
	enabled     bool
	tokens      float64
	lastRefill  time.Time
	rate        float64
	burst       float64
	nextAllowed time.Time
}

const (
	fallbackRequestsPerMinute = 60
	fallbackBurst             = 10
	maxRateLimitWait          = 5 * time.Minute
)

// RateLimitError reports a retryable Hardcover limit response. RetryAfter is
// zero when the server did not supply a reset time.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("hardcover: rate limited; retry after %s", e.RetryAfter.Round(time.Second))
	}
	return "hardcover: rate limited"
}

func (e *RateLimitError) Temporary() bool { return true }

// Retryable reports whether an error should leave queue work pending. HTTP
// rate limits and server failures are retried; invalid requests are not.
func Retryable(err error) bool {
	var rateLimit *RateLimitError
	if errors.As(err, &rateLimit) {
		return true
	}
	var temporary interface{ Temporary() bool }
	if errors.As(err, &temporary) && temporary.Temporary() {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) || errors.Is(err, context.DeadlineExceeded)
}

// New creates a Client using the given Hardcover personal API key (from
// https://hardcover.app/account/api). The key needs the read:catalog scope;
// it is sent as a bearer credential. enabled is loaded from
// users.IntegrationSettings at startup. Use SetConfig to update either at
// runtime.
func New(enabled bool, token string) *Client {
	return NewWithTransport(enabled, token, nil)
}

// NewWithTransport creates a Client with transport. A nil transport uses the
// default HTTP transport. The production timeout remains 30 seconds.
func NewWithTransport(enabled bool, token string, transport http.RoundTripper) *Client {
	return &Client{
		token:      token,
		enabled:    enabled,
		http:       &http.Client{Transport: transport, Timeout: 30 * time.Second},
		tokens:     fallbackBurst,
		lastRefill: time.Now(),
		rate:       float64(fallbackRequestsPerMinute) / 60,
		burst:      fallbackBurst,
	}
}

// SetConfig updates the client's enabled state and token in place — called
// after the admin Integrations page saves a change, so the running
// background queue and any in-flight manual checks pick it up immediately.
func (c *Client) SetConfig(enabled bool, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = enabled
	c.token = token
}

// Enabled reports whether Hardcover is both turned on and has a token
// configured — callers should skip enrichment entirely (not error) when
// this is false, since Hardcover integration is optional.
func (c *Client) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled && c.token != ""
}

type graphqlRequest struct {
	Query     string `json:"query"`
	Variables any    `json:"variables,omitempty"`
}

type graphqlError struct {
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, query string, variables any, out any) error {
	if err := c.throttle(ctx); err != nil {
		return err
	}

	body, err := json.Marshal(graphqlRequest{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("hardcover: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("hardcover: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.currentToken())
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hardcover: request failed: %w", err)
	}
	defer resp.Body.Close()
	c.updateRateLimit(resp.Header)

	if resp.StatusCode == http.StatusTooManyRequests {
		return &RateLimitError{RetryAfter: retryAfter(resp.Header, time.Now())}
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("hardcover: server status %d: %w", resp.StatusCode, &temporaryError{})
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hardcover: unexpected status %d", resp.StatusCode)
	}

	data, err := readResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("hardcover: decode response: %w", err)
	}

	var envelope struct {
		Errors []graphqlError  `json:"errors"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("hardcover: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		if strings.Contains(strings.ToLower(envelope.Errors[0].Message), "rate limit") {
			return &RateLimitError{RetryAfter: retryAfter(resp.Header, time.Now())}
		}
		return fmt.Errorf("hardcover: %s", envelope.Errors[0].Message)
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("hardcover: decode data: %w", err)
		}
	}
	return nil
}

func readResponse(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return data, nil
}

type temporaryError struct{}

func (*temporaryError) Error() string   { return "temporary failure" }
func (*temporaryError) Temporary() bool { return true }

// throttle applies Hardcover's advertised allowance when known. Before a
// response supplies headers, it uses a 60 request/minute token bucket with a
// burst of ten requests.
func (c *Client) throttle(ctx context.Context) error {
	for {
		c.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(c.lastRefill).Seconds()
		c.tokens = min(c.burst, c.tokens+elapsed*c.rate)
		c.lastRefill = now
		wait := c.nextAllowed.Sub(now)
		if wait <= 0 && c.tokens >= 1 {
			c.tokens--
			c.mu.Unlock()
			return nil
		}
		if wait <= 0 {
			wait = time.Duration((1 - c.tokens) / c.rate * float64(time.Second))
		}
		c.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *Client) updateRateLimit(headers http.Header) {
	limit, ok := rateLimitInt(headers, "RateLimit-Limit", "X-RateLimit-Limit")
	remaining, hasRemaining := rateLimitInt(headers, "RateLimit-Remaining", "X-RateLimit-Remaining")
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	if ok && limit > 0 {
		c.rate = float64(limit) / 60
		c.burst = float64(limit)
		if c.burst < 1 {
			c.burst = 1
		}
	}
	if hasRemaining {
		c.tokens = min(c.tokens, float64(max(remaining, 0)))
		if remaining <= 0 {
			if retry := retryAfter(headers, now); retry > 0 {
				c.nextAllowed = now.Add(retry)
			}
		}
	}
	if retry := retryAfter(headers, now); retry > 0 {
		log.Printf("hardcover: respecting server rate-limit cooldown of %s", retry.Round(time.Second))
		if retryAt := now.Add(retry); retryAt.After(c.nextAllowed) {
			c.nextAllowed = retryAt
		}
	}
}

func rateLimitInt(headers http.Header, names ...string) (int, bool) {
	for _, name := range names {
		if value, err := strconv.Atoi(headers.Get(name)); err == nil && value >= 0 {
			return value, true
		}
	}
	return 0, false
}

func retryAfter(headers http.Header, now time.Time) time.Duration {
	if value := strings.TrimSpace(headers.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return boundedRateLimitWait(time.Duration(seconds) * time.Second)
		}
		if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
			return boundedRateLimitWait(retryAt.Sub(now))
		}
	}
	for _, name := range []string{"RateLimit-Reset", "X-RateLimit-Reset"} {
		value := strings.TrimSpace(headers.Get(name))
		if value == "" {
			continue
		}
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds < 0 {
			continue
		}
		if seconds > now.Unix()*100 {
			seconds /= 1000 // Some APIs return a Unix epoch in milliseconds.
		}
		if seconds > now.Unix() {
			return boundedRateLimitWait(time.Unix(seconds, 0).Sub(now))
		}
		return boundedRateLimitWait(time.Duration(seconds) * time.Second)
	}
	return 0
}

func boundedRateLimitWait(wait time.Duration) time.Duration {
	if wait > maxRateLimitWait {
		return maxRateLimitWait
	}
	return wait
}
