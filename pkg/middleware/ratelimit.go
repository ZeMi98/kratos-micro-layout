// Package middleware builds the reusable, transport-agnostic request
// middleware this template ships.
//
// It currently provides rate limiting in two shapes that share one limiter
// implementation:
//
//   - RateLimitServer — a kratos middleware.Middleware for backend services,
//     whose routes run through the generated handler chain. It can use kratos's
//     adaptive BBR limiter (the default) or a fixed token bucket.
//   - RateLimitFilter — an http filter for the gateway, whose raw
//     reverse-proxy handlers (registered with HandlePrefix) bypass kratos's
//     middleware chain and can only be wrapped by filters. It uses a token
//     bucket, the standard fixed-quota pattern for an edge.
//
// Circuit breaking is intentionally not here: kratos v3 ships only a
// client-side breaker, and this template's real client path is the gateway's
// per-upstream reverse proxy, so the breaker lives with that proxy
// (app/gateway/internal/proxy) where it can observe backend status codes.
package middleware

import (
	"io"
	"net/http"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/ratelimit"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"golang.org/x/time/rate"
)

// Limiter kinds understood by a service's config `type` field.
const (
	// KindBBR selects kratos's adaptive BBR limiter (the default). BBR
	// self-tunes its ceiling from observed latency and CPU load, so it needs no
	// explicit numbers. kratos keeps its constructor internal, so BBR is only
	// reachable through ratelimit.Server's default — not as a standalone
	// limiter — which is why the gateway filter cannot offer it.
	KindBBR = "bbr"
	// KindToken selects a fixed token bucket that enforces an explicit
	// qps/burst ceiling.
	KindToken = "token"
)

// defaultQPS backs the token bucket when a config enables it but leaves qps
// unset, so a half-filled config still limits at a sane rate instead of
// allowing everything (or nothing).
const defaultQPS = 100

// RateLimitServer builds the server-side rate-limit middleware for a backend
// service. It returns nil when disabled, so callers append it only after a nil
// check and keep the rest of the chain untouched.
//
// kind selects the limiter: KindToken (case-insensitive) uses a fixed token
// bucket at qps/burst; anything else — including the empty string and KindBBR
// — uses kratos's adaptive BBR default.
func RateLimitServer(enabled bool, kind string, qps, burst int) middleware.Middleware {
	if !enabled {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(kind), KindToken) {
		return ratelimit.Server(ratelimit.WithLimiter(NewTokenLimiter(qps, burst)))
	}
	return ratelimit.Server() // BBR default.
}

// RateLimitFilter builds an HTTP filter that applies a token bucket to every
// request reaching the gateway. Rejected requests are answered 429 with the
// same JSON shape the gateway's other edge errors use. It returns nil when
// disabled so the caller can skip appending the filter.
//
// The filter shares one bucket across all routes: kratos's ratelimit.Limiter is
// context-free (Allow takes no request), so per-route or per-client limiting
// belongs in a dedicated edge component, not here.
func RateLimitFilter(enabled bool, qps, burst int) khttp.FilterFunc {
	if !enabled {
		return nil
	}
	limiter := NewTokenLimiter(qps, burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			done, err := limiter.Allow()
			if err != nil {
				writeTooManyRequests(w)
				return
			}
			// A token bucket accounts for a request when it is admitted, so
			// the completion callback carries no extra signal.
			defer done(ratelimit.DoneInfo{})
			next.ServeHTTP(w, r)
		})
	}
}

// NewTokenLimiter builds a token-bucket limiter sustaining qps requests per
// second with a maximum burst capacity. Non-positive qps falls back to
// defaultQPS; non-positive burst falls back to qps (a short peak equal to one
// second of sustained traffic).
func NewTokenLimiter(qps, burst int) ratelimit.Limiter {
	if qps <= 0 {
		qps = defaultQPS
	}
	if burst <= 0 {
		burst = qps
	}
	return &tokenBucket{limiter: rate.NewLimiter(rate.Limit(qps), burst)}
}

// tokenBucket adapts golang.org/x/time/rate.Limiter to kratos's
// ratelimit.Limiter so the well-tested stdlib-adjacent bucket can drive both
// the middleware and the filter.
type tokenBucket struct {
	limiter *rate.Limiter
}

// Allow implements ratelimit.Limiter. It reserves one token, returning
// ratelimit.ErrLimitExceed when the bucket is empty. The DoneFunc is a no-op:
// unlike BBR, a token bucket does not learn from request completion.
func (t *tokenBucket) Allow() (ratelimit.DoneFunc, error) {
	if !t.limiter.Allow() {
		return nil, ratelimit.ErrLimitExceed
	}
	return func(ratelimit.DoneInfo) {}, nil
}

// writeTooManyRequests answers a rate-limited request at the gateway edge,
// matching the JSON envelope the proxy's 502/503 errors use.
func writeTooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = io.WriteString(w, `{"code":429,"message":"rate limit exceeded","data":null}`)
}
