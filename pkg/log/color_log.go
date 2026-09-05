package log

// color_log.go implements a minimal, colored slog.Handler with no third-party
// dependencies. It is selected by Options.Format == "color" in log.go and is
// intended for local development; production deployments typically use "json".
//
// Output looks like:
//   2026-09-05T15:04:05.123Z INFO  server listening addr=[::]:8000
//   2026-09-05T15:04:05.456Z ERROR query failed err="connection refused"
//
// Design notes:
//   - The timestamp and source location are dim, only the level tag is colored
//     (bold + hue); everything else is left plain so it stays readable and
//     doesn't fight with terminal themes.
//   - Empty string attrs are dropped (e.g. trace_id="" span_id="" that
//     slog-based libraries often attach as zero values) to cut noise in dev logs.
//   - Attrs are flattened to dotted keys honoring slog group semantics: an attr
//     added before a WithGroup stays at its original level, only later attrs
//     nest under the group — so WithAttrs/WithGroup compose like the stdlib.
//   - Color is auto-disabled when NO_COLOR is set, TERM=dumb, or the handler
//     isn't writing to a real stdout/stderr (e.g. file output) — callers can
//     also force it off via Options.NoColor.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	ansiReset  = "\033[0m"
	ansiGray   = "\033[90m"
	ansiBlue   = "\033[34m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiBold   = "\033[1m"
)

// ColorHandlerOptions controls ColorHandler behavior.
type ColorHandlerOptions struct {
	Level     slog.Leveler // minimum enabled level, default Info
	AddSource bool         // include the source file:line, resolved from the record PC
	NoColor   bool         // force-disable color regardless of auto-detection
}

// ColorHandler is a minimal slog.Handler that prints colored, single-line
// records intended for local development.
type ColorHandler struct {
	opts  ColorHandlerOptions
	w     io.Writer
	mu    *sync.Mutex
	attrs []slog.Attr // pre-flattened: each Key already carries its dotted group path
	group string      // group prefix applied to record attrs and to future WithAttrs
	color bool
}

// NewColorHandler creates a ColorHandler writing to w.
func NewColorHandler(w io.Writer, opts *ColorHandlerOptions) *ColorHandler {
	if opts == nil {
		opts = &ColorHandlerOptions{}
	}
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}
	return &ColorHandler{
		opts:  *opts,
		w:     w,
		mu:    &sync.Mutex{},
		color: !opts.NoColor && isColorCapable(w),
	}
}

// isColorCapable is a dependency-free heuristic: respects NO_COLOR
// (https://no-color.org/) and TERM=dumb, and otherwise only allows color
// when writing directly to os.Stdout or os.Stderr — file/pipe destinations
// (e.g. lumberjack rotation) fall back to plain text automatically.
func isColorCapable(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return w == io.Writer(os.Stdout) || w == io.Writer(os.Stderr)
}

// Enabled reports whether the handler emits records at level.
func (h *ColorHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

// Handle renders one colored, single-line record. The whole line is built in a
// buffer and written under a shared mutex, so concurrent goroutines never
// interleave partial lines.
func (h *ColorHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	b.WriteString(h.colorize(ansiGray, r.Time.Format(time.RFC3339Nano)))
	b.WriteByte(' ')
	b.WriteString(h.levelTag(r.Level))
	if h.opts.AddSource {
		if src := sourceLocation(r.PC); src != "" {
			b.WriteByte(' ')
			b.WriteString(h.colorize(ansiGray, src))
		}
	}
	b.WriteByte(' ')
	b.WriteString(r.Message)

	// Stored attrs are already flattened with their group baked into the key,
	// so they render with an empty group; record attrs nest under the current one.
	for _, a := range h.attrs {
		writeColorAttr(&b, "", a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeColorAttr(&b, h.group, a)
		return true
	})

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// WithAttrs returns a handler whose future records also carry attrs. Each attr
// is flattened now, against the group active at this point, so a later WithGroup
// cannot retroactively re-parent it (matching stdlib slog semantics).
func (h *ColorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := *h
	nh.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	nh.attrs = append(nh.attrs, h.attrs...)
	for _, a := range attrs {
		nh.attrs = appendColorAttr(nh.attrs, a, h.group)
	}
	return &nh
}

// WithGroup returns a handler that nests subsequent attrs under name. An empty
// name is a no-op, per the slog.Handler contract.
func (h *ColorHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	nh.group = joinGroup(h.group, name)
	return &nh
}

func (h *ColorHandler) colorize(color, s string) string {
	if !h.color {
		return s
	}
	return color + s + ansiReset
}

// levelTag renders a fixed-width, colored level label so columns line up.
func (h *ColorHandler) levelTag(level slog.Level) string {
	var color, tag string
	switch {
	case level >= slog.LevelError:
		color, tag = ansiRed, "ERROR"
	case level >= slog.LevelWarn:
		color, tag = ansiYellow, "WARN"
	case level >= slog.LevelInfo:
		color, tag = ansiGreen, "INFO"
	default:
		color, tag = ansiBlue, "DEBUG"
	}
	return h.colorize(ansiBold+color, fmt.Sprintf("%-5s", tag))
}

// sourceLocation renders the record PC as "file.go:line", or "" when the PC is
// unset (a record built without runtime.Callers, e.g. some synthetic records).
func sourceLocation(pc uintptr) string {
	if pc == 0 {
		return ""
	}
	fs := runtime.CallersFrames([]uintptr{pc})
	f, _ := fs.Next()
	if f.File == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", filepath.Base(f.File), f.Line)
}

// joinGroup joins a group prefix and a name with a dot, tolerating either side
// being empty so no leading or trailing dots are produced.
func joinGroup(prefix, name string) string {
	switch {
	case prefix == "":
		return name
	case name == "":
		return prefix
	default:
		return prefix + "." + name
	}
}

// appendColorAttr flattens a into dst under group: group-valued attrs expand
// into dotted keys, and each stored attr's Key is fully qualified so Handle can
// write it with an empty group. Empty attrs are skipped.
func appendColorAttr(dst []slog.Attr, a slog.Attr, group string) []slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return dst
	}
	if a.Value.Kind() == slog.KindGroup {
		g := joinGroup(group, a.Key)
		for _, ga := range a.Value.Group() {
			dst = appendColorAttr(dst, ga, g)
		}
		return dst
	}
	return append(dst, slog.Attr{Key: joinGroup(group, a.Key), Value: a.Value})
}

// writeColorAttr renders one attr as " key=value" under group. Empty attrs and
// empty-string values (trace_id="" and friends) are dropped; group-valued attrs
// recurse under the joined group; values containing whitespace or quotes are
// quoted so the line stays parseable by eye.
func writeColorAttr(b *strings.Builder, group string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		g := joinGroup(group, a.Key)
		for _, ga := range a.Value.Group() {
			writeColorAttr(b, g, ga)
		}
		return
	}
	if a.Value.Kind() == slog.KindString && a.Value.String() == "" {
		return
	}

	b.WriteByte(' ')
	b.WriteString(joinGroup(group, a.Key))
	b.WriteByte('=')

	val := a.Value.String()
	if strings.ContainsAny(val, " \t\"") {
		val = fmt.Sprintf("%q", val)
	}
	b.WriteString(val)
}
