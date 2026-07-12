package middleware

import "net/http"

// CORS returns middleware that permits browser clients from allowedOrigin to call
// the gateway, answering the preflight OPTIONS request the browser sends before a
// cross-origin request that carries a JSON body or the X-API-Key header.
func CORS(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Advertise to the browser which origin, methods, and headers are
			// permitted, and which response header the client's JS may read.
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
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
