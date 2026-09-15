// Package logging builds the logger every other package shares.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New builds the process logger.
//
// Text is the default because the first audience for these lines is a person
// watching a terminal or `journalctl`; JSON is available for installations
// that ship logs somewhere that parses them.
func New(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	if strings.EqualFold(format, "json") {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

// parseLevel maps a configured name onto a level, defaulting to info for
// anything unrecognised so a typo does not silence the process.
func parseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
