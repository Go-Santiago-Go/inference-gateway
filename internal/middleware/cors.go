package middleware

import "net/http"

// CORS returns middleware that permits browser clients from any of allowedOrigins
// to call the gateway, answering the preflight OPTIONS request the browser sends
// before a cross-origin request that carries a JSON body or the X-API-Key header.
//
// The request's Origin is echoed back only when it is in the allowlist, because
// Access-Control-Allow-Origin names a single origin: reflecting the caller's own
// origin is how one handler serves several permitted origins (for example both
// localhost and 127.0.0.1 in local dev) without allowing all of them.
func CORS(allowedOrigins ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				// Caches must vary on Origin now that the header is request-dependent.
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
			w.Header().Set("Access-Control-Expose-Headers", "Retry-After")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
