// Package server builds the gateway HTTP server: one listener, a CORS filter,
// an optional edge rate-limit filter, and one reverse-proxy handler per
// configured route — each optionally guarded by its own circuit breaker.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"kratos-micro-layout/app/gateway/internal/conf"
	"kratos-micro-layout/app/gateway/internal/proxy"
	pkgmw "kratos-micro-layout/pkg/middleware"

	"github.com/go-kratos/kratos/v3/registry"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// New builds the HTTP server and wires every route to its upstream. The
// returned cleanup closes all upstream watchers.
func New(c *conf.Server, g *conf.Gateway, mw *conf.Middleware, disc registry.Discovery, logger *slog.Logger) (*khttp.Server, func(), error) {
	opts := []khttp.ServerOption{
		khttp.Address(c.GetHttp().GetAddr()),
	}
	if d := c.GetHttp().GetTimeout().AsDuration(); d > 0 {
		opts = append(opts, khttp.Timeout(d))
	}
	// Filters run outermost-first, so CORS is registered before rate limiting:
	// a rate-limited 429 must still carry access-control headers or the browser
	// cannot read the rejection.
	if cors := g.GetCors(); cors != nil {
		opts = append(opts, khttp.Filter(corsFilter(cors)))
	}
	// Rate limiting is a filter, not kratos middleware: the proxy routes below
	// are raw handlers registered with HandlePrefix, which bypass the middleware
	// chain that only generated (proto) handlers enter.
	if rl := pkgmw.RateLimitFilter(
		mw.GetRatelimit().GetEnabled(),
		int(mw.GetRatelimit().GetQps()),
		int(mw.GetRatelimit().GetBurst()),
	); rl != nil {
		opts = append(opts, khttp.Filter(rl))
	}
	srv := khttp.NewServer(opts...)

	// One breaker configuration is shared by every route; each upstream still
	// gets its own breaker instance, so a sick backend trips only its own
	// circuit and leaves the healthy routes serving.
	breaker := breakerSettings(mw.GetCircuitBreaker())

	var upstreams []*proxy.Upstream
	cleanup := func() {
		for _, u := range upstreams {
			if err := u.Close(); err != nil {
				logger.Warn("failed closing upstream", "upstream", u.Name(), "err", err)
			}
		}
	}
	for _, route := range g.GetRoutes() {
		prefix, service := route.GetPathPrefix(), route.GetService()
		if prefix == "" || service == "" {
			cleanup()
			return nil, nil, fmt.Errorf("gateway: every route needs path_prefix and service, got prefix=%q service=%q", prefix, service)
		}
		u, err := proxy.NewUpstream(context.Background(), disc, proxy.HTTPServiceName(service), logger)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("gateway: route %q: %w", prefix, err)
		}
		upstreams = append(upstreams, u)
		srv.HandlePrefix(prefix, proxy.NewHandler(u, prefix, route.GetRewritePrefix(), breaker, logger))
		logger.Info("route registered", "prefix", prefix, "service", service)
	}
	return srv, cleanup, nil
}

// breakerSettings translates the config circuit-breaker block into proxy
// settings, or nil when it is absent or disabled — in which case every upstream
// forwards without a breaker. Field defaults (max_requests, interval, timeout,
// failure_ratio, min_requests) are applied inside the proxy, so this only maps
// the configured values across.
func breakerSettings(c *conf.CircuitBreaker) *proxy.BreakerSettings {
	if c == nil || !c.GetEnabled() {
		return nil
	}
	return &proxy.BreakerSettings{
		MaxRequests:  c.GetMaxRequests(),
		Interval:     c.GetInterval().AsDuration(),
		Timeout:      c.GetTimeout().AsDuration(),
		FailureRatio: c.GetFailureRatio(),
		MinRequests:  c.GetMinRequests(),
	}
}

// corsFilter answers preflight requests and decorates real responses with
// access-control headers.
func corsFilter(c *conf.Cors) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || !originAllowed(c, origin) {
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			if c.GetAllowCredentials() {
				h.Set("Access-Control-Allow-Credentials", "true")
			}
			// A preflight carries Access-Control-Request-Method; answer it
			// without forwarding to the backend.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if methods := c.GetAllowMethods(); len(methods) > 0 {
					h.Set("Access-Control-Allow-Methods", strings.Join(methods, ", "))
				}
				if headers := c.GetAllowHeaders(); len(headers) > 0 {
					h.Set("Access-Control-Allow-Headers", strings.Join(headers, ", "))
				}
				if maxAge := c.GetMaxAge().AsDuration(); maxAge > 0 {
					h.Set("Access-Control-Max-Age", strconv.Itoa(int(maxAge.Seconds())))
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originAllowed reports whether the policy admits the request origin.
func originAllowed(c *conf.Cors, origin string) bool {
	for _, o := range c.GetAllowOrigins() {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}
