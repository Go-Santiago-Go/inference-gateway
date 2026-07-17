package middleware

import (
	"math"
	"net/http"
	"strconv"
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

			// Reserve claims the next token and reports how long until it would be
			// available. A zero delay means a token was free, so the request
			// proceeds. A positive delay means an empty bucket: cancel the
			// reservation (the request is rejected, not queued) and reject with a
			// 429 before next, so a throttled request never reaches Bedrock. The
			// delay becomes an honest Retry-After the client can count down.
			res := lim.(*rate.Limiter).Reserve()
			if delay := res.Delay(); delay > 0 {
				res.Cancel()
				// Round up to whole seconds, floored at 1, since Retry-After is an
				// integer count of seconds and 0 would tell the client to retry now.
				seconds := max(int(math.Ceil(delay.Seconds())), 1)
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
