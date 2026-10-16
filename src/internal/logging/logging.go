// Package logging provides the application's shared structured logging entry point.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

const ComponentKey = "component"

// LevelHTTP is an opt-in access-log level. It remains above debug so
// LOG_LEVEL=debug enables it while normal application logs retain their usual
// debug/info/warn/error levels.
const LevelHTTP slog.Level = slog.LevelDebug + 1

// ParseLevel parses the levels supported by LOG_LEVEL.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid LOG_LEVEL %q (want debug, info, warn, or error)", value)
	}
}

// New creates the process logger. Logs are written as structured text to stderr.
func New(level string) (*slog.Logger, error) {
	parsed, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	return NewWithWriter(os.Stderr, parsed), nil
}

// NewWithWriter is intended for tests and tools that need a different sink.
func NewWithWriter(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(&lineHandler{writer: w, level: level, mu: new(sync.Mutex)})
}

// Configure installs logger as the process logger used by For.
func Configure(logger *slog.Logger) {
	if logger != nil {
		slog.SetDefault(logger)
	}
}

// For returns the shared logger with a stable fully-qualified component name
// derived from value's concrete type. Pass a typed nil pointer for a package's
// long-lived component, for example For((*web.Server)(nil)).
func For(value any) *slog.Logger {
	return slog.Default().With(ComponentKey, componentName(value))
}

// Or returns logger when non-nil, otherwise a shared component logger.
func Or(logger *slog.Logger, component any) *slog.Logger {
	if logger != nil {
		return logger
	}
	return For(component)
}

func componentName(value any) string {
	t := reflect.TypeOf(value)
	if t == nil {
		return "unknown"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.PkgPath() == "" || t.Name() == "" {
		return t.String()
	}
	return t.PkgPath() + "." + t.Name()
}

func redact(groups []string, attr slog.Attr) slog.Attr {
	if sensitiveKey(attr.Key) {
		return slog.String(attr.Key, "[REDACTED]")
	}
	if attr.Value.Kind() == slog.KindAny {
		return slog.Any(attr.Key, redactAny(attr.Value.Any()))
	}
	return attr
}

func redactAny(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			if sensitiveKey(key) {
				result[key] = "[REDACTED]"
			} else {
				result[key] = redactAny(item)
			}
		}
		return result
	case map[string]string:
		result := make(map[string]string, len(value))
		for key, item := range value {
			if sensitiveKey(key) {
				result[key] = "[REDACTED]"
			} else {
				result[key] = item
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = redactAny(item)
		}
		return result
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(key))
	for _, word := range []string{"token", "apikey", "authorization", "password", "secret", "session", "cookie", "credential"} {
		if strings.Contains(key, word) {
			return true
		}
	}
	return false
}

const internalPrefix = "github.com/sethwv/my-sideload-library/internal/"

// lineHandler formats records for people reading a terminal or container log:
// time level component message key=value. It deliberately keeps the stable
// component near the start of the line and leaves structured context after it.
type lineHandler struct {
	writer io.Writer
	level  slog.Level
	attrs  []slog.Attr
	groups []string
	mu     *sync.Mutex
}

func (h *lineHandler) Enabled(_ context.Context, level slog.Level) bool { return level >= h.level }

func (h *lineHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := append([]slog.Attr{}, h.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)
		return true
	})

	component, fields := "", make([]string, 0, len(attrs))
	values := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		if attr.Key == ComponentKey && attr.Value.Kind() == slog.KindString {
			component = displayComponent(attr.Value.String())
			continue
		}
		formatted := formatAttr(attr, h.groups)
		fields = append(fields, formatted...)
		for _, field := range formatted {
			if key, value, ok := strings.Cut(field, "="); ok {
				values[key] = value
			}
		}
	}
	if record.Level == LevelHTTP {
		return h.handleHTTP(record.Time, values)
	}

	var line strings.Builder
	line.WriteString(record.Time.Format("2006-01-02 15:04:05.000"))
	line.WriteByte(' ')
	line.WriteString(record.Level.String())
	if component != "" {
		line.WriteByte(' ')
		line.WriteString(component)
		line.WriteByte(':')
	}
	line.WriteByte(' ')
	line.WriteString(record.Message)
	for _, field := range fields {
		line.WriteByte(' ')
		line.WriteString(field)
	}
	line.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.writer, line.String())
	return err
}

func (h *lineHandler) handleHTTP(timestamp time.Time, values map[string]string) error {
	var line strings.Builder
	line.WriteString(timestamp.Format("2006-01-02 15:04:05.000"))
	line.WriteString(" HTTP ")
	line.WriteString(values["status"])
	line.WriteByte(' ')
	line.WriteString(values["method"])
	line.WriteByte(' ')
	line.WriteString(unquoteValue(values["route"]))
	for _, key := range []string{"bytes", "duration", "xhr"} {
		if value := values[key]; value != "" {
			line.WriteByte(' ')
			line.WriteString(key)
			line.WriteByte('=')
			line.WriteString(value)
		}
	}
	line.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.writer, line.String())
	return err
}

func unquoteValue(value string) string {
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return value
}

func (h *lineHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *lineHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

func displayComponent(component string) string {
	return strings.TrimPrefix(component, internalPrefix)
}

func formatAttr(attr slog.Attr, groups []string) []string {
	attr = redact(groups, attr)
	if attr.Value.Kind() == slog.KindGroup {
		result := []string{}
		for _, child := range attr.Value.Group() {
			result = append(result, formatAttr(child, append(groups, attr.Key))...)
		}
		return result
	}
	key := strings.Join(append(groups, attr.Key), ".")
	return []string{key + "=" + formatValue(attr.Value)}
}

func formatValue(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		return quoteValue(value.String())
	case slog.KindTime:
		return value.Time().Format(time.RFC3339Nano)
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindBool:
		return strconv.FormatBool(value.Bool())
	case slog.KindInt64:
		return strconv.FormatInt(value.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(value.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(value.Float64(), 'f', -1, 64)
	default:
		return quoteValue(fmt.Sprint(value.Any()))
	}
}

func quoteValue(value string) string {
	if strings.ContainsAny(value, " \t\r\n=\"") || value == "" {
		return strconv.Quote(value)
	}
	return value
}

// GooseLogger adapts Goose's printf-style logger to the application's
// structured logs and identifies the database migration domain.
type GooseLogger struct {
	logger *slog.Logger
	domain string
}

func NewGooseLogger(domain string) *GooseLogger {
	return &GooseLogger{logger: slog.Default().With(ComponentKey, "goose"), domain: domain}
}

func (l *GooseLogger) Printf(format string, args ...any) {
	l.logger.Info(gooseMessage(l.domain, format, args...))
}

func (l *GooseLogger) Fatalf(format string, args ...any) {
	l.logger.Error(gooseMessage(l.domain, format, args...))
}

func gooseMessage(domain, format string, args ...any) string {
	message := fmt.Sprintf(format, args...)
	message = strings.TrimSpace(strings.TrimPrefix(message, "goose:"))
	return domain + " - " + message
}
