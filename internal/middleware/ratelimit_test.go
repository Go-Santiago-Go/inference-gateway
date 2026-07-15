package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"
)

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
