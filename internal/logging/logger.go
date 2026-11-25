// Package logging builds the application's structured logger.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a slog.Logger writing to the given writer at the given level, in
// either text or JSON form.
func New(level, format string, writer io.Writer) *slog.Logger {
	options := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	if strings.EqualFold(format, "text") {
		handler = slog.NewTextHandler(writer, options)
	} else {
		handler = slog.NewJSONHandler(writer, options)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
