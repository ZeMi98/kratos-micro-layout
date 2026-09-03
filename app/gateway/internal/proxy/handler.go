package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v3/selector"
	"github.com/sony/gobreaker/v2"
)

// nodeKey carries the selected node from ServeHTTP into the ReverseProxy
// director, which runs separately and cannot take extra arguments.
type nodeKey struct{}

func withNode(ctx context.Context, node selector.Node) context.Context {
	return context.WithValue(ctx, nodeKey{}, node)
}

func nodeFrom(ctx context.Context) selector.Node {
	node, _ := ctx.Value(nodeKey{}).(selector.Node)
	return node
}

// Outcome sentinels reported by forward to the circuit breaker. They never
// reach the client — forward already wrote the HTTP response — they only tell
// the breaker whether the call counted as a backend failure.
var (
	// errUpstream marks a backend 5xx response, the only outcome that counts as
	// a circuit-breaker failure.
	errUpstream = errors.New("upstream returned a server error")
	// errNoNode marks a discovery gap (no healthy instance registered). It is
	// excluded from failures so a registry blip cannot trip the breaker.
	errNoNode = errors.New("no upstream node available")
)

// BreakerSettings configures one upstream's circuit breaker. A nil
// *BreakerSettings disables circuit breaking; zero-valued fields fall back to
// the defaults noted per field. The gateway builds this from
// middleware.circuit_breaker so the proxy package never imports config types.
type BreakerSettings struct {
	// MaxRequests is how many calls are let through while half-open to probe
	// recovery. Default 5.
	MaxRequests uint32
	// Interval is how often the closed-state counters reset, so the failure
	// ratio reflects a recent window rather than all history. Default 30s.
	Interval time.Duration
	// Timeout is how long the breaker stays open before probing half-open.
	// Default 30s.
	Timeout time.Duration
	// FailureRatio (0..1) of failed calls that trips the breaker once at least
	// MinRequests were observed. Default 0.6.
	FailureRatio float64
	// MinRequests is the sample size required before FailureRatio can trip, so
	// a couple of early errors on an idle breaker do not open it. Default 20.
	MinRequests uint32
}

// Handler reverse-proxies every request it receives to one instance of a single
// upstream service. The httputil.ReverseProxy is built once and reused;
// per-request state (the selected node) travels through the request context.
// When a breaker is configured, calls are wrapped in it so a persistently
// failing backend is shed with a fast 503 instead of being piled onto.
type Handler struct {
	upstream      *Upstream
	pathPrefix    string
	rewritePrefix string
	proxy         *httputil.ReverseProxy
	breaker       *gobreaker.CircuitBreaker[struct{}]
	logger        *slog.Logger
}

// NewHandler builds the proxy handler for one route. breaker may be nil to
// disable circuit breaking for this upstream.
func NewHandler(u *Upstream, pathPrefix, rewritePrefix string, breaker *BreakerSettings, logger *slog.Logger) *Handler {
	h := &Handler{
		upstream:      u,
		pathPrefix:    pathPrefix,
		rewritePrefix: rewritePrefix,
		logger:        logger.With("upstream", u.name, "prefix", pathPrefix),
	}
	h.proxy = &httputil.ReverseProxy{
		Director:     h.director,
		ErrorHandler: h.errorHandler,
	}
	h.breaker = newBreaker(u.name, breaker, h.logger)
	return h
}

// newBreaker builds the gobreaker for one upstream, applying defaults to any
// unset BreakerSettings field. It returns nil when s is nil (breaking off).
func newBreaker(name string, s *BreakerSettings, logger *slog.Logger) *gobreaker.CircuitBreaker[struct{}] {
	if s == nil {
		return nil
	}
	maxRequests := orDefault(s.MaxRequests, 5)
	interval := orDefaultDuration(s.Interval, 30*time.Second)
	timeout := orDefaultDuration(s.Timeout, 30*time.Second)
	failureRatio := s.FailureRatio
	if failureRatio <= 0 {
		failureRatio = 0.6
	}
	minRequests := orDefault(s.MinRequests, 20)

	return gobreaker.NewCircuitBreaker[struct{}](gobreaker.Settings{
		Name:        name,
		MaxRequests: maxRequests,
		Interval:    interval,
		Timeout:     timeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			// Trip only on a sustained failure ratio over a real sample, so a
			// brief blip on low traffic does not open the circuit.
			if c.Requests < minRequests {
				return false
			}
			return float64(c.TotalFailures)/float64(c.Requests) >= failureRatio
		},
		IsSuccessful: func(err error) bool {
			// Only a backend 5xx is a failure; a discovery gap (errNoNode) and a
			// clean proxy (nil) are not backend faults.
			return !errors.Is(err, errUpstream)
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			logger.Warn("circuit breaker state changed",
				"breaker", name, "from", from.String(), "to", to.String())
		},
	})
}

func orDefault(v, def uint32) uint32 {
	if v == 0 {
		return def
	}
	return v
}

func orDefaultDuration(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

// director rewrites the outbound request onto the selected node. The
// httputil.ReverseProxy itself adds X-Forwarded-For and strips hop-by-hop
// headers.
func (h *Handler) director(req *http.Request) {
	node := nodeFrom(req.Context())
	if node == nil {
		return // unreachable: forward always attaches a node
	}
	req.URL.Scheme = node.Scheme()
	req.URL.Host = node.Address()
	if h.rewritePrefix != "" {
		req.URL.Path = h.rewritePrefix + strings.TrimPrefix(req.URL.Path, h.pathPrefix)
	}
}

// errorHandler answers when the backend connection itself fails. A transport
// error surfaces as 502; the ReverseProxy reports it here after writing nothing.
func (h *Handler) errorHandler(w http.ResponseWriter, _ *http.Request, err error) {
	h.logger.Error("upstream request failed", "err", err)
	h.writeJSON(w, http.StatusBadGateway, "upstream request failed")
}

// ServeHTTP forwards one request, guarded by the circuit breaker when present.
// When the breaker is open it rejects the call before any bytes are written, so
// ServeHTTP answers a fast 503; otherwise forward has already written the
// client response (success or backend error) and only the breaker accounting
// remains.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.breaker == nil {
		_ = h.forward(w, r)
		return
	}
	if _, err := h.breaker.Execute(func() (struct{}, error) {
		return struct{}{}, h.forward(w, r)
	}); errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		h.writeJSON(w, http.StatusServiceUnavailable, "upstream circuit open")
	}
}

// forward selects one healthy instance and proxies the request to it, always
// writing the client response itself. Its return value feeds the breaker only:
// errUpstream on a backend 5xx, errNoNode when discovery has no instance, and
// nil on a forwarded (non-5xx) response.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request) error {
	node, done, err := h.upstream.Select(r.Context())
	if err != nil {
		h.logger.Warn("no upstream node available")
		h.writeJSON(w, http.StatusServiceUnavailable, "no upstream node available")
		return errNoNode
	}

	ctx := withNode(r.Context(), node)
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	h.proxy.ServeHTTP(sw, r.WithContext(ctx))
	// Report the outcome so latency-aware balancers (p2c and friends) stay
	// accurate; random's DoneFunc is a no-op.
	done(ctx, selector.DoneInfo{Err: sw.err})

	h.logger.Info("proxied",
		"method", r.Method,
		"path", r.URL.Path,
		"node", node.Address(),
		"status", sw.status,
		"duration", time.Since(start).String(),
	)

	// 5xx means the backend is unhealthy; anything below it (including 4xx,
	// which is the client's fault) is a successful proxy round-trip.
	if sw.status >= http.StatusInternalServerError {
		return errUpstream
	}
	return nil
}

// writeJSON answers with a small JSON body, matching the shape the gateway's
// edge errors use so clients parse one format regardless of the failure.
func (h *Handler) writeJSON(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"code":%d,"message":%q}`, status, message)
}

// statusWriter records the response status and write errors so proxy access
// logs and balancer callbacks can observe them.
type statusWriter struct {
	http.ResponseWriter
	status int
	err    error
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		w.err = err
	}
	return n, err
}

// Flush keeps streaming responses flowing through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
