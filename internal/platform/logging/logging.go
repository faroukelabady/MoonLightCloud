// Package logging builds the single structured slog logger.
// Output goes to stdout/stderr as JSON so containers and hosts just collect
// the stream. No vendor dependency. Secrets are never logged: callers must
// not put credentials, tokens, or DATABASE_URL passwords into fields.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New builds a JSON slog logger at the given level.
func New(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
