package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	for input, want := range map[string]slog.Level{"": slog.LevelInfo, "DEBUG": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		got, err := ParseLevel(input)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = (%v, %v), want (%v, nil)", input, got, err, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Fatal("ParseLevel accepted invalid level")
	}
}

func TestLoggerRedactsSensitiveAttributes(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter(&out, slog.LevelDebug)
	logger.Info("test", "api_token", "top-secret", "safe", "value", slog.Group("provider", slog.String("password", "also-secret")), "metadata", map[string]any{"nested": map[string]string{"session_id": "third-secret"}})
	got := out.String()
	for _, secret := range []string{"top-secret", "also-secret", "third-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("log contains secret %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "safe=value") {
		t.Fatalf("unexpected log output: %s", got)
	}
}

func TestForUsesQualifiedType(t *testing.T) {
	type component struct{}
	if got := componentName((*component)(nil)); got != "github.com/sethwv/my-sideload-library/internal/logging.component" {
		t.Fatalf("componentName() = %q", got)
	}
}

func TestGooseLoggerIncludesMigrationDomain(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter(&out, slog.LevelInfo)
	goose := &GooseLogger{logger: logger, domain: "library"}
	goose.Printf("no migrations to run. current version: %d", 12)
	got := out.String()
	for _, want := range []string{"library - no migrations to run. current version: 12"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}
