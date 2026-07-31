package middleware

import (
	"net/http"
	"time"

	"github.com/Go-Santiago-Go/inference-gateway/internal/metrics"
)

// Metrics wraps next and records the count and latency of every request it
// serves. It reuses statusRecorder so the response status becomes a label,
// which is what makes the 401, 429 and 502 rates queryable without a separate
// collector per failure mode.
//
// The path is normalized through metrics.Route before it becomes a label: the
// raw r.URL.Path would let any caller mint an unbounded number of time series
// by requesting random paths.
func Metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		metrics.RecordRequest(r.Method, metrics.Route(r.URL.Path), rec.status, time.Since(start))
	})
}
