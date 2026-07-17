package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"golang.org/x/time/rate"
)

// TestRateLimitRejectsWithRetryAfter pins the rejection path: once a key's bucket
// is empty, the next request is rejected with 429 before reaching next, carrying a
// Retry-After computed from the real refill delay (never 0, which would tell the
// client to retry immediately into the same empty bucket).
func TestRateLimitRejectsWithRetryAfter(t *testing.T) {
	served := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	})
	// Burst 1, refill one token every 2s, so the second immediate request finds an
	// empty bucket and must wait ~2s.
	h := RateLimit(0.5, 1)(next)

	newReq := func() (*http.Request, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
		req = req.WithContext(context.WithValue(req.Context(), keyID, "k"))
		return req, httptest.NewRecorder()
	}

	req, rec := newReq()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec.Code)
	}

	served = false
	req, rec = newReq()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rec.Code)
	}
	if served {
		t.Error("rejected request reached next; it must be shed in middleware")
	}
	secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After = %q, want an integer", rec.Header().Get("Retry-After"))
	}
	if secs < 1 {
		t.Errorf("Retry-After = %d, want >= 1", secs)
	}
}

// BenchmarkRateLimit measures the per-request overhead the rate-limit middleware
// adds on the allow path: reading the key from context, the sync.Map lookup, and
// the token-bucket Allow call. rate.Inf makes every request pass so the number
// reflects steady-state overhead, not the cost of a rejection.
func BenchmarkRateLimit(b *testing.B) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h := RateLimit(rate.Inf, 1)(next)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
	req = req.WithContext(context.WithValue(req.Context(), keyID, "benchkey"))
	rec := httptest.NewRecorder()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkRateLimitParallel measures the same overhead under concurrent load,
// exercising the sync.Map read path that a mutex-guarded map would serialize.
func BenchmarkRateLimitParallel(b *testing.B) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h := RateLimit(rate.Inf, 1)(next)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
		req = req.WithContext(context.WithValue(req.Context(), keyID, "benchkey"))
		rec := httptest.NewRecorder()
		for pb.Next() {
			h.ServeHTTP(rec, req)
		}
	})
}
