// Package log builds the service logger from a shared Options using the
// standard library log/slog handlers — TextHandler or JSONHandler — with
// optional lumberjack rotation for file output. Every call site gets a
// *slog.Logger, so kratos's slog-native log pipeline (and the otel trace
// attrs layered on top in cmd) work without adapters.
package log

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Options configures the logger.
type Options struct {
	// Level is the minimum enabled level: "debug", "info", "warn", "error".
	// Default "info".
	Level string
	// Format selects the record encoding: "text" or "json". Default "text".
	Format string
	// Output selects the destination: "stdout", "stderr", or "file".
	// Default "stdout".
	Output string
	// FilePath is the log file path used when Output is "file". The file is
	// rotated by size with lumberjack.
	FilePath string
	// AddSource includes the source file and line in each record.
	AddSource bool
}

// ParseLevel converts a level string into a slog.Level. Unknown values fall
// back to info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
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

// Writer resolves the output target. When Output is "file" the writer is a
// rotating lumberjack logger; the returned cleanup flushes and closes it. For
// stdout/stderr the cleanup is a no-op.
func (o Options) Writer() (io.Writer, func()) {
	switch strings.ToLower(strings.TrimSpace(o.Output)) {
	case "stderr":
		return os.Stderr, func() {}
	case "file":
		lw := &lumberjack.Logger{
			Filename:   o.FilePath,
			MaxSize:    100, // megabytes
			MaxBackups: 5,
			MaxAge:     30, // days
			Compress:   true,
		}
		return lw, func() { _ = lw.Close() }
	default:
		return os.Stdout, func() {}
	}
}

// New builds a *slog.Logger from opts using the stdlib Text or JSON handler
// (Format "json" selects JSON, anything else Text). The returned cleanup
// closes the underlying writer when output goes to a file.
func New(opts Options) (*slog.Logger, func(), error) {
	w, cleanup := opts.Writer()
	hopts := &slog.HandlerOptions{
		Level:     ParseLevel(opts.Level),
		AddSource: opts.AddSource,
	}
	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(opts.Format), "json") {
		handler = slog.NewJSONHandler(w, hopts)
	} else {
		handler = slog.NewTextHandler(w, hopts)
	}
	return slog.New(handler), cleanup, nil
}
