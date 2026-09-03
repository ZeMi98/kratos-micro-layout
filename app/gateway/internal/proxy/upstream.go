// Package proxy provides the gateway forwarding core: an Upstream keeps a
// balanced view of one backend service's healthy instances from the registry,
// and a Handler reverse-proxies HTTP requests to the selected instance —
// optionally guarded by a per-upstream circuit breaker that sheds a persistently
// failing backend with a fast 503.
package proxy

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/selector"
	"github.com/go-kratos/kratos/v3/selector/random"
)

// HTTPServiceName returns the Nacos service name for a backend's HTTP
// transport. Kratos registers each transport as its own Nacos service —
// "user_center.http" and "user_center.grpc" — so route entries only need the
// plain service name and the ".http" kind is appended here unless already
// present.
func HTTPServiceName(service string) string {
	if strings.HasSuffix(service, ".http") || strings.HasSuffix(service, ".grpc") {
		return service
	}
	return service + ".http"
}

// Upstream tracks the instances of one backend service and balances requests
// across them. It watches the registry in the background and feeds every
// change into the selector.
type Upstream struct {
	name    string
	disc    registry.Discovery
	sel     selector.Selector
	watcher registry.Watcher
	cancel  context.CancelFunc
	logger  *slog.Logger
}

// NewUpstream resolves the current instances of the service, then keeps them
// fresh through a registry watcher. A service without live instances yet is
// not an error — the gateway answers 503 until instances appear.
func NewUpstream(ctx context.Context, disc registry.Discovery, service string, logger *slog.Logger) (*Upstream, error) {
	ctx, cancel := context.WithCancel(ctx)
	u := &Upstream{
		name:   service,
		disc:   disc,
		sel:    random.New(),
		cancel: cancel,
		logger: logger.With("upstream", service),
	}
	// Prime the node list synchronously so early requests need not race the
	// watcher's first callback.
	if err := u.refresh(ctx); err != nil {
		u.logger.Warn("initial discovery lookup failed", "err", err)
	}
	w, err := disc.Watch(ctx, service)
	if err != nil {
		cancel()
		return nil, err
	}
	u.watcher = w
	go u.loop()
	return u, nil
}

// Name returns the registry service name this upstream tracks.
func (u *Upstream) Name() string {
	return u.name
}

// Select picks one healthy instance for a request.
func (u *Upstream) Select(ctx context.Context) (selector.Node, selector.DoneFunc, error) {
	return u.sel.Select(ctx)
}

// Close stops the watcher and the background refresh loop.
func (u *Upstream) Close() error {
	u.cancel()
	if u.watcher != nil {
		return u.watcher.Stop()
	}
	return nil
}

// refresh pulls the current instance list into the selector.
func (u *Upstream) refresh(ctx context.Context) error {
	ins, err := u.disc.GetService(ctx, u.name)
	if err != nil {
		return err
	}
	u.apply(ins)
	return nil
}

// loop consumes watcher events until the upstream is closed.
func (u *Upstream) loop() {
	for {
		ins, err := u.watcher.Next()
		if err != nil {
			// Either the context was cancelled on Close, or the registry
			// watcher failed; both mean this goroutine should exit.
			return
		}
		u.apply(ins)
	}
}

// apply converts registry instances into selector nodes.
func (u *Upstream) apply(ins []*registry.ServiceInstance) {
	nodes := make([]selector.Node, 0, len(ins))
	for _, in := range ins {
		for _, ep := range in.Endpoints {
			epURL, err := url.Parse(ep)
			if err != nil {
				u.logger.Warn("skipping malformed endpoint", "endpoint", ep, "err", err)
				continue
			}
			nodes = append(nodes, selector.NewNode(epURL.Scheme, epURL.Host, in))
		}
	}
	u.sel.Apply(nodes)
	u.logger.Info("upstream instances updated", "instances", len(ins), "nodes", len(nodes))
}
