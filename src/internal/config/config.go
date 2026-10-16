// Package config loads server configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sethwv/my-sideload-library/internal/logging"
)

type Config struct {
	LibraryPaths  []string
	DataDir       string
	SessionSecret string
	Port          string
	SiteName      string
	CoverWidth    int
	PageSize      int
	SessionTTL    time.Duration
	LogLevel      string
	LogHTTP       bool
	// HardcoverToken is a one-time bootstrap value only: on first boot, if
	// no integration_settings row exists yet, it seeds the DB-backed
	// Hardcover token (see users.IntegrationSettings and the admin
	// Integrations page) so existing deployments don't lose their token on
	// upgrade. After that first seed, the DB is the live source of truth —
	// this field is never read again.
	HardcoverToken string
	// PublicURL is the trusted base URL (e.g. "https://library.example.com")
	// used to build links emailed to users (password reset, invites). Left
	// empty, password-reset and invite emails are not sent because the app
	// refuses to construct externally delivered links from a request Host
	// header. Set this in any deployment that sends email.
	PublicURL string
}

func Load() (*Config, error) {
	c := &Config{
		LibraryPaths:   splitPaths(getenv("LIBRARY_PATH", "/library")),
		DataDir:        DataDir(),
		SessionSecret:  os.Getenv("SESSION_SECRET"),
		Port:           getenv("PORT", "8080"),
		SiteName:       getenv("SITE_NAME", "sideload-library"),
		HardcoverToken: os.Getenv("HARDCOVER_API_TOKEN"),
		PublicURL:      strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"),
		LogLevel:       getenv("LOG_LEVEL", "info"),
	}

	var err error
	if c.CoverWidth, err = getenvInt("COVER_WIDTH", 300); err != nil {
		return nil, err
	}
	if c.PageSize, err = getenvInt("PAGE_SIZE", 48); err != nil {
		return nil, err
	}
	if c.SessionTTL, err = getenvDuration("SESSION_TTL", 720*time.Hour); err != nil {
		return nil, err
	}
	if c.LogHTTP, err = getenvBool("LOG_HTTP", false); err != nil {
		return nil, err
	}
	if _, err = logging.ParseLevel(c.LogLevel); err != nil {
		return nil, err
	}

	if len(c.LibraryPaths) == 0 {
		return nil, fmt.Errorf("LIBRARY_PATH must be set")
	}
	return c, nil
}

// DataDir returns the directory holding all persistent application state.
// Administrative commands use it without needing the server configuration.
func DataDir() string {
	return getenv("DATA_DIR", "/data")
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitPaths parses a comma-separated LIBRARY_PATH into a cleaned,
// deduplicated list of directories, so config.LibraryPaths can be compared
// or joined without callers re-normalizing.
func splitPaths(v string) []string {
	var paths []string
	seen := make(map[string]bool)
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
}

func getenvInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return n, nil
}

func getenvDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return d, nil
}

func getenvBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %w", key, err)
	}
	return b, nil
}
