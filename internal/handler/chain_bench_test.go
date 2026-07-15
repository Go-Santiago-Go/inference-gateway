package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/time/rate"

	"github.com/Go-Santiago-Go/inference-gateway/internal/bedrock"
	"github.com/Go-Santiago-Go/inference-gateway/internal/middleware"
)

// benchWriter is a throwaway ResponseWriter that discards all output while still
// satisfying http.Flusher, so the streaming handler runs its real flush path
// without a growing buffer skewing the measurement.
type benchWriter struct{ h http.Header }

func (b *benchWriter) Header() http.Header {
	if b.h == nil {
		b.h = http.Header{}
	}
	return b.h
}
func (b *benchWriter) Write(p []byte) (int, error) { return len(p), nil }
func (b *benchWriter) WriteHeader(int)             {}
func (b *benchWriter) Flush()                      {}

// buildChain wires the full production middleware chain from main.go around the
// streaming handler, backed by the fake generator so no AWS call is made.
func buildChain() http.Handler {
	gen := fakeGenerator{comp: bedrock.Completion{Text: "hello world", TokensIn: 1500, TokensOut: 800}}
	h := New(gen, "us.anthropic.claude-haiku-4-5-20251001-v1:0")

	cors := middleware.CORS("http://localhost:5173")
	auth := middleware.Auth(map[string]bool{"benchkey": true})
	// rate.Inf so the limiter never rejects; this measures throughput of the
	// serving path, not the rate cap.
	rateLimit := middleware.RateLimit(rate.Inf, 1)

	return middleware.Logging(cors(auth(rateLimit(http.HandlerFunc(h.ChatStream)))))
}

// BenchmarkChainThroughput measures requests/second through the entire gateway
// pipeline (logging, CORS, auth, rate limit, SSE handler, metering) with Bedrock
// faked out, so the number reflects the gateway's own overhead. Logs are
// discarded because in production they go to CloudWatch, not the hot path.
func BenchmarkChainThroughput(b *testing.B) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	chain := buildChain()

	const body = `{"prompt":"hi"}`
	w := &benchWriter{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
	req.Header.Set("X-API-Key", "benchkey")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req.Body = io.NopCloser(strings.NewReader(body))
		chain.ServeHTTP(w, req)
	}
}

// BenchmarkChainThroughputParallel runs the same path across GOMAXPROCS
// goroutines for an aggregate sustained-throughput figure under concurrency.
func BenchmarkChainThroughputParallel(b *testing.B) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	chain := buildChain()

	const body = `{"prompt":"hi"}`
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		w := &benchWriter{}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
		req.Header.Set("X-API-Key", "benchkey")
		for pb.Next() {
			req.Body = io.NopCloser(strings.NewReader(body))
			chain.ServeHTTP(w, req)
		}
	})
}
