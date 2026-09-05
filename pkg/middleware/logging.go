package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

// RequestLogger returns a middleware that logs one line per RPC: transport kind,
// operation, result code/reason and latency. Severity tracks the outcome —
// success at info, a 4xx (client fault) at warn, and a 5xx or unknown/unwrapped
// error at error — so alerting can key on error level without firing on client
// mistakes.
//
// Unlike the stock logging.Server middleware it deliberately omits the request
// payload: services often carry credentials (Register / Login / ChangePassword),
// and dumping arguments would write plain-text passwords into the logs. The
// logger is the kratos-decorated one from main, so trace_id and span_id are
// attached automatically when a span is active.
func RequestLogger(logger *slog.Logger) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (reply any, err error) {
			var kind, operation string
			if tr, ok := transport.FromServerContext(ctx); ok {
				kind = tr.Kind().String()
				operation = tr.Operation()
			}
			start := time.Now()
			reply, err = handler(ctx, req)

			var code int32
			var reason, message string
			if se := errors.FromError(err); se != nil {
				code = se.Code
				reason = se.Reason
				message = se.Message
			}
			attrs := []slog.Attr{
				slog.String("kind", kind),
				slog.String("operation", operation),
				slog.Int64("code", int64(code)),
				slog.String("reason", reason),
				slog.Float64("latency", time.Since(start).Seconds()),
			}

			level := slog.LevelInfo
			switch {
			case err == nil:
				level = slog.LevelInfo
			case code >= 500 || code == 0: // 0 usually means an unknown/unwrapped error; treat it as server-side too
				level = slog.LevelError
			default: // 4xx: a client-side problem, not a service fault
				level = slog.LevelWarn
			}

			msg := "request"
			if err != nil {
				attrs = append(attrs, slog.String("error", message))
				msg = "request failed"
			}
			logger.LogAttrs(ctx, level, msg, attrs...)

			return reply, err
		}
	}
}
