// Package log builds the service logger from a shared Options using the
// standard library log/slog handlers — TextHandler, JSONHandler, or the
// local ColorHandler (color_log.go) — with optional lumberjack rotation for
// file output. Every call site gets a *slog.Logger, so kratos's slog-native
// log pipeline (and the otel trace attrs layered on top in cmd) work without
// adapters.
package log

import (
	"fmt"
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
	// Format selects the record encoding: "text", "json", or "color".
	// "color" prints minimal, single-line, ANSI-colored records intended for
	// local development (see color_log.go); color is automatically disabled
	// when writing to a file, when NO_COLOR is set, or when NoColor is true.
	// Default "text".
	Format string
	// Output selects the destination: "stdout", "stderr", or "file".
	// Default "stdout".
	Output string
	// FilePath is the log file path used when Output is "file". The file is
	// rotated by size with lumberjack. Required when Output is "file".
	FilePath string
	// AddSource includes the source file and line in each record.
	AddSource bool
	// NoColor forces color off even when Format is "color", e.g. for CI
	// environments where auto-detection isn't reliable.
	NoColor bool
	// Rotation controls size-based rotation of the log file and applies only
	// when Output is "file". Zero values defer to lumberjack's own defaults.
	Rotation RotationOptions
}

// RotationOptions maps onto lumberjack's rotation knobs. Every field is
// optional; a zero value falls back to lumberjack's default (MaxSize 100 MB;
// MaxBackups and MaxAge 0 retain everything; Compress off). Services set these
// from config so production retention is explicit rather than hardcoded.
type RotationOptions struct {
	// MaxSize is the size in megabytes at which a file is rotated. Default 100.
	MaxSize int
	// MaxBackups is the number of rotated files to retain. 0 retains all.
	MaxBackups int
	// MaxAge is the number of days to retain rotated files. 0 retains all.
	MaxAge int
	// Compress gzips rotated files.
	Compress bool
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
// rotating lumberjack logger configured from Rotation; the returned cleanup
// flushes and closes it. For stdout/stderr the cleanup is a no-op.
func (o Options) Writer() (io.Writer, func()) {
	switch strings.ToLower(strings.TrimSpace(o.Output)) {
	case "stderr":
		return os.Stderr, func() {}
	case "file":
		lw := &lumberjack.Logger{
			Filename:   o.FilePath,
			MaxSize:    o.Rotation.MaxSize,    // megabytes; 0 -> lumberjack default (100)
			MaxBackups: o.Rotation.MaxBackups, // 0 -> retain all
			MaxAge:     o.Rotation.MaxAge,     // days; 0 -> retain all
			Compress:   o.Rotation.Compress,
		}
		return lw, func() { _ = lw.Close() }
	default:
		return os.Stdout, func() {}
	}
}

// New builds a *slog.Logger from opts. Format "json" selects the stdlib JSON
// handler, "color" selects the local ColorHandler, and anything else
// (including the default "") falls back to the stdlib Text handler. The
// returned cleanup closes the underlying writer when output goes to a file.
// It returns an error when Output is "file" but FilePath is empty, which would
// otherwise make lumberjack silently log to an os.TempDir() default.
func New(opts Options) (*slog.Logger, func(), error) {
	if strings.EqualFold(strings.TrimSpace(opts.Output), "file") && strings.TrimSpace(opts.FilePath) == "" {
		return nil, func() {}, fmt.Errorf("log: output %q requires a non-empty file_path", opts.Output)
	}

	w, cleanup := opts.Writer()
	level := ParseLevel(opts.Level)

	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(opts.Format)) {
	case "json":
		handler = slog.NewJSONHandler(w, &slog.HandlerOptions{
			Level:     level,
			AddSource: opts.AddSource,
		})
	case "color":
		handler = NewColorHandler(w, &ColorHandlerOptions{
			Level:     level,
			AddSource: opts.AddSource,
			NoColor:   opts.NoColor,
		})
	default:
		handler = slog.NewTextHandler(w, &slog.HandlerOptions{
			Level:     level,
			AddSource: opts.AddSource,
		})
	}

	return slog.New(handler), cleanup, nil
}
