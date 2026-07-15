package middleware

import (
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

// RateLimit returns middleware that throttles requests per API key using a
// token bucket. Each key gets its own limiter allowing burst requests to be
// absorbed while capping the sustained rate at rps. A key whose bucket is empty
// is rejected with 429 and a Retry-After header before the request reaches the
// handler. Auth must run earlier in the chain so the key is on the context.
func RateLimit(rps rate.Limit, burst int) func(http.Handler) http.Handler {
	// limiters holds one *rate.Limiter per key, created on first sight and read
	// on every later request. sync.Map fits this write-once, read-many pattern
	// and is safe under the concurrent requests a plain map would crash on.
	var limiters sync.Map

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := KeyFromContext(r.Context())

			// LoadOrStore returns the existing limiter or atomically installs a
			// new one, so racing first-requests for a key can't each create a
			// separate bucket.  The stored value is any, hence the assertion.
			lim, _ := limiters.LoadOrStore(key, rate.NewLimiter(rps, burst))

			// Allow consumes a token without blocking. An empty bucket rejects
			// with 429 and returns before next, so a throttled request never
			// reaches Bedrock. Retry-After is a fixed hint; a Reserve-based
			// exact delay is deferred as a refinement.
			if !lim.(*rate.Limiter).Allow() {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
