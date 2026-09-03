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
// operation, result code/reason and latency. Failures are logged at error level
// with the message.
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
			if err != nil {
				attrs = append(attrs, slog.String("error", message))
				logger.LogAttrs(ctx, slog.LevelError, "request failed", attrs...)
			} else {
				logger.LogAttrs(ctx, slog.LevelInfo, "request", attrs...)
			}
			return reply, err
		}
	}
}
