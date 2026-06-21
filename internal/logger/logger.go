// Package logger configures the application-wide slog logger.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// Setup configures the default slog logger with the given level and returns it.
// Level is one of: debug, info, warn, error.
func Setup(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
	})
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}
