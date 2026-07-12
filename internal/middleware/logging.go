// Package middleware holds the request pipeline: cross-cutting concerns
// (logging, CORS, auth, rate limiting) that wrap every request so handlers
// stay thin. Each middleware is a func(http.Handler) http.Handler, which lets
// them compose into a chain.
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Logging wraps next and emits one structured log line per request, recording
// the request method, path, and the latency of the wrapped handler.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Measure latency across the handler: stamp before, log after.
		start := time.Now()

		logger := slog.With("request_id", newRequestID())
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"latency_ms", time.Since(start).Milliseconds(),
		)
	})
}

// newRequestID returns a random 16-byte hex string used to correlate every log
// line emitted while handling a single request.
func newRequestID() string {
	b := make([]byte, 16)
	// crypto/rand.Read only fails if the OS entropy source is broken, which a
	// healthy server never hits; a degraded ID is acceptable for correlation.
	rand.Read(b)
	return hex.EncodeToString(b)
}
