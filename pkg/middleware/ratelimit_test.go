package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-kratos/kratos/v3/middleware/ratelimit"
)

func TestRateLimitServerDisabledIsNil(t *testing.T) {
	if mw := RateLimitServer(false, KindToken, 10, 10); mw != nil {
		t.Fatal("a disabled limiter must build no middleware so the chain is untouched")
	}
}

func TestRateLimitServerEnabledIsNotNil(t *testing.T) {
	// Both kinds must produce a middleware: token (explicit numbers) and bbr
	// (the default, reached through ratelimit.Server's own limiter).
	if mw := RateLimitServer(true, KindToken, 10, 10); mw == nil {
		t.Fatal("enabled token limiter must build a middleware")
	}
	if mw := RateLimitServer(true, KindBBR, 0, 0); mw == nil {
		t.Fatal("enabled bbr limiter must build a middleware")
	}
	if mw := RateLimitServer(true, "", 0, 0); mw == nil {
		t.Fatal("an empty kind defaults to bbr and must build a middleware")
	}
}

func TestRateLimitFilterDisabledIsNil(t *testing.T) {
	if f := RateLimitFilter(false, 10, 10); f != nil {
		t.Fatal("a disabled limiter must build no filter")
	}
}

func TestTokenLimiterAllowsBurstThenRejects(t *testing.T) {
	// qps=1, burst=2: two immediate admissions drain the bucket, the third is
	// rejected because a token takes a full second to refill.
	l := NewTokenLimiter(1, 2)
	if _, err := l.Allow(); err != nil {
		t.Fatalf("first allow: %v", err)
	}
	done, err := l.Allow()
	if err != nil {
		t.Fatalf("second allow: %v", err)
	}
	done(ratelimit.DoneInfo{}) // the no-op completion must be safe to call
	if _, err := l.Allow(); err == nil {
		t.Fatal("third allow must be rejected once the burst is exhausted")
	}
}

func TestTokenLimiterDefaultsAreSane(t *testing.T) {
	// Non-positive inputs must not build a zero-rate bucket that rejects
	// everything; the first call should still be admitted.
	l := NewTokenLimiter(0, 0)
	if _, err := l.Allow(); err != nil {
		t.Fatalf("default limiter should admit the first request: %v", err)
	}
}

func TestRateLimitFilterRejectsOverLimit(t *testing.T) {
	// qps=1, burst=1: the second back-to-back request is shed with 429.
	f := RateLimitFilter(true, 1, 1)
	next := f(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec1 := httptest.NewRecorder()
	next.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "http://gateway/v1/users", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	next.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "http://gateway/v1/users", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: status = %d, want 429", rec2.Code)
	}
	if ct := rec2.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("429 Content-Type = %q, want application/json", ct)
	}
}
