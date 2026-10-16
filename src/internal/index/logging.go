package index

import (
	"log/slog"

	"github.com/sethwv/my-sideload-library/internal/logging"
)

var logger = logging.For((*DB)(nil))

func componentLogger(value *slog.Logger) *slog.Logger {
	return logging.Or(value, (*DB)(nil))
}
