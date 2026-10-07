package web

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const maxCoverBytes = 20 << 20 // 20MB

var lookupIP = net.LookupIP

// applyCoverFromURL fetches a cover through a pinned, publicly-routable HTTPS
// address, then stores it through the normal cover cache.
func (s *Server) applyCoverFromURL(ctx context.Context, bookID int64, rawURL string) error {
	data, mediaType, err := fetchCover(ctx, rawURL)
	if err != nil {
		return err
	}
	path, err := s.Covers.SaveCover(bookID, data, mediaType)
	if err != nil {
		return err
	}
	return s.DB.SetCover(bookID, path)
}

func fetchCover(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, requestHost, serverName, err := resolveCoverURL(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("refusing to fetch cover url: %w", err)
	}

	// Build the request URL from the vetted connection target. The path and query
	// select a cover on that server, but cannot change where the client connects.
	requestURL := &url.URL{
		Scheme:     "https",
		Host:       u.Host,
		Path:       u.Path,
		RawPath:    u.RawPath,
		ForceQuery: u.ForceQuery,
		RawQuery:   u.RawQuery,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Host = requestHost
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{ServerName: serverName},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", &httpStatusError{resp.StatusCode}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func resolveCoverURL(rawURL string) (*url.URL, string, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", "", err
	}
	if u.Scheme != "https" || u.User != nil {
		return nil, "", "", fmt.Errorf("URL must be an https URL without credentials")
	}
	host := u.Hostname()
	if host == "" {
		return nil, "", "", fmt.Errorf("missing host")
	}
	requestHost := u.Host

	addrs, err := lookupIP(host)
	if err != nil {
		return nil, "", "", fmt.Errorf("resolve host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, "", "", fmt.Errorf("host %q did not resolve to any address", host)
	}
	for _, ip := range addrs {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return nil, "", "", fmt.Errorf("host %q resolves to a non-public address (%s)", host, ip)
		}
	}

	ip := addrs[0]
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(ip.String(), port)
	} else {
		u.Host = ip.String()
	}
	return u, requestHost, host, nil
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string {
	return "unexpected status " + strconv.Itoa(e.code)
}
