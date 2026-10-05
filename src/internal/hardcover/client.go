// Package hardcover is a minimal client for the Hardcover GraphQL API
// (https://docs.hardcover.app/api/), used to fill in and clean up book
// metadata (series, release date, title) that a library's EPUB files often
// don't have or get wrong.
package hardcover

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// endpoint is variable for legacy package tests. New tests can instead inject
// a transport through NewWithTransport without mutating package state.
var endpoint = "https://api.hardcover.app/v1/graphql"

const maxResponseBytes = 10 << 20

const userAgent = "my-sideload-library"

// Client is safe for concurrent use — every call goes through a shared rate
// limiter (Hardcover's own limit is 60 requests/min) regardless of which
// goroutine (the background enrichment queue, or a manual per-book check)
// is asking, so nothing needs its own separate throttling logic. token/
// enabled are mutable (see SetConfig) so the admin Integrations page can
// flip Hardcover on/off or rotate its token without a server restart; the
// same mutex that guards the rate limiter's lastCallTime also guards these.
type Client struct {
	http *http.Client

	mu           sync.Mutex
	token        string
	enabled      bool
	lastCallTime time.Time
}

// minInterval is slightly more than 1/60th of a minute, for a safety margin
// under Hardcover's 60 req/min limit.
const minInterval = 1100 * time.Millisecond

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
		token:   token,
		enabled: enabled,
		http:    &http.Client{Transport: transport, Timeout: 30 * time.Second},
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
	c.throttle()

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

// throttle blocks until it's been at least minInterval since the previous
// call, serializing every request through this client regardless of caller.
func (c *Client) throttle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if wait := minInterval - time.Since(c.lastCallTime); wait > 0 {
		time.Sleep(wait)
	}
	c.lastCallTime = time.Now()
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}
