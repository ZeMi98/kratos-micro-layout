package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/registry"
)

// fakeDiscovery hands out a fixed instance list and never pushes updates.
type fakeDiscovery struct {
	ins []*registry.ServiceInstance
}

func (f *fakeDiscovery) GetService(_ context.Context, _ string) ([]*registry.ServiceInstance, error) {
	return f.ins, nil
}

func (f *fakeDiscovery) Watch(context.Context, string) (registry.Watcher, error) {
	return &fakeWatcher{}, nil
}

type fakeWatcher struct{}

func (fakeWatcher) Next() ([]*registry.ServiceInstance, error) {
	<-context.Background().Done()
	return nil, context.Canceled
}

func (fakeWatcher) Stop() error { return nil }

// newBackend starts an httptest server echoing which instance served the
// request plus the original method, path, and body.
func newBackend(t *testing.T, name string) (*httptest.Server, *registry.ServiceInstance) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s|%s|%s|%s", name, r.Method, r.URL.Path, string(body))
	}))
	t.Cleanup(srv.Close)
	ins := &registry.ServiceInstance{
		ID:        name,
		Name:      "user_center.http",
		Version:   "v1",
		Metadata:  map[string]string{"kind": "http", "version": "v1"},
		Endpoints: []string{srv.URL},
	}
	return srv, ins
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHandlerProxiesRequest(t *testing.T) {
	_, ins := newBackend(t, "backend-a")
	disc := &fakeDiscovery{ins: []*registry.ServiceInstance{ins}}

	u, err := NewUpstream(context.Background(), disc, ins.Name, testLogger())
	if err != nil {
		t.Fatalf("NewUpstream: %v", err)
	}
	t.Cleanup(func() { _ = u.Close() })

	h := NewHandler(u, "/v1/users", "", nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "http://gateway/v1/users/123?verbose=true", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d, body %q", got, want, rec.Body.String())
	}
	// The method and full path survive the hop through the gateway.
	if got, want := rec.Body.String(), "backend-a|GET|/v1/users/123|"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHandlerRewritesPrefix(t *testing.T) {
	_, ins := newBackend(t, "backend-b")
	disc := &fakeDiscovery{ins: []*registry.ServiceInstance{ins}}

	u, err := NewUpstream(context.Background(), disc, ins.Name, testLogger())
	if err != nil {
		t.Fatalf("NewUpstream: %v", err)
	}
	t.Cleanup(func() { _ = u.Close() })

	h := NewHandler(u, "/api/user", "/v1", nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "http://gateway/api/user/9", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if got, want := rec.Body.String(), "backend-b|GET|/v1/9|"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHandlerNoNodeAvailable(t *testing.T) {
	disc := &fakeDiscovery{} // service with no instances
	u, err := NewUpstream(context.Background(), disc, "user_center.http", testLogger())
	if err != nil {
		t.Fatalf("NewUpstream: %v", err)
	}
	t.Cleanup(func() { _ = u.Close() })

	h := NewHandler(u, "/v1/users", "", nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "http://gateway/v1/users", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHandlerBadGatewayOnDeadBackend(t *testing.T) {
	srv, ins := newBackend(t, "backend-c")
	disc := &fakeDiscovery{ins: []*registry.ServiceInstance{ins}}

	u, err := NewUpstream(context.Background(), disc, ins.Name, testLogger())
	if err != nil {
		t.Fatalf("NewUpstream: %v", err)
	}
	t.Cleanup(func() { _ = u.Close() })

	// The registry still lists the instance, but nothing listens anymore.
	srv.Close()

	h := NewHandler(u, "/v1/users", "", nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "http://gateway/v1/users", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusBadGateway; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHandlerBalancesAcrossInstances(t *testing.T) {
	_, a := newBackend(t, "node-a")
	_, b := newBackend(t, "node-b")
	disc := &fakeDiscovery{ins: []*registry.ServiceInstance{a, b}}

	u, err := NewUpstream(context.Background(), disc, a.Name, testLogger())
	if err != nil {
		t.Fatalf("NewUpstream: %v", err)
	}
	t.Cleanup(func() { _ = u.Close() })

	h := NewHandler(u, "/v1/users", "", nil, testLogger())
	served := sync.Map{}
	for i := 0; i < 40; i++ {
		req := httptest.NewRequest(http.MethodGet, "http://gateway/v1/users", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
		served.Store(rec.Body.String()[:6], true)
	}
	if _, ok := served.Load("node-a"); !ok {
		t.Error("node-a never served a request; balancer is not spreading load")
	}
	if _, ok := served.Load("node-b"); !ok {
		t.Error("node-b never served a request; balancer is not spreading load")
	}
}

func TestHTTPServiceName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"user_center", "user_center.http"},
		{"user_center.http", "user_center.http"},
		{"user_center.grpc", "user_center.grpc"},
	}
	for _, c := range cases {
		if got := HTTPServiceName(c.in); got != c.want {
			t.Errorf("HTTPServiceName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHandlerCircuitBreakerOpensOnRepeatedFailures(t *testing.T) {
	// A backend that always answers 500 and counts how often it is actually hit.
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	ins := &registry.ServiceInstance{
		ID:        "sick",
		Name:      "user_center.http",
		Version:   "v1",
		Metadata:  map[string]string{"kind": "http"},
		Endpoints: []string{srv.URL},
	}
	disc := &fakeDiscovery{ins: []*registry.ServiceInstance{ins}}
	u, err := NewUpstream(context.Background(), disc, ins.Name, testLogger())
	if err != nil {
		t.Fatalf("NewUpstream: %v", err)
	}
	t.Cleanup(func() { _ = u.Close() })

	// Trip fast: at least 3 samples and a 50% failure ratio (every call fails).
	h := NewHandler(u, "/v1/users", "", &BreakerSettings{
		MinRequests:  3,
		FailureRatio: 0.5,
		MaxRequests:  1,
		Interval:     time.Minute,
		Timeout:      time.Minute,
	}, testLogger())

	// Drive traffic until the breaker opens and starts shedding with 503.
	var opened bool
	for i := 0; i < 12; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://gateway/v1/users", nil))
		if rec.Code == http.StatusServiceUnavailable && strings.Contains(rec.Body.String(), "circuit open") {
			opened = true
			break
		}
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("call %d: status = %d, want 500 (backend) or 503 (open circuit)", i, rec.Code)
		}
	}
	if !opened {
		t.Fatal("breaker never opened after repeated backend 500s")
	}

	// Once open, the gateway sheds without touching the sick backend: the hit
	// count must not grow across several more calls.
	before := atomic.LoadInt64(&hits)
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://gateway/v1/users", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("shed call %d: status = %d, want 503", i, rec.Code)
		}
	}
	if after := atomic.LoadInt64(&hits); after != before {
		t.Fatalf("backend hits grew from %d to %d while the circuit was open", before, after)
	}
}
