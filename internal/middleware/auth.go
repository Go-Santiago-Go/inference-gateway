package middleware

import (
	"context"
	"net/http"
)

// contextKey is an unexported type for keys stored in a request's context.
// Using a private named type (rather than a bare string) guarantees no other
// package can collide with these keys, since no outside code can name the type.
type contextKey string

// keyID labels the authenticated API key attached to the request context by
// Auth and read back by downstream middleware (e.g. Logging) and handlers.
const keyID contextKey = "api-key"

// Auth returns middleware that authenticates request by the X-API-Key header.
// A missing or unknown key is rejected with 401 before the request reaches the
// handler; a valid key is attached to the request context under keyID so
// downstream middle and handlers can attribute the request to a caller.
func Auth(keys map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("X-API-Key")
			if !keys[key] {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), keyID, key)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// KeyFromContext returns the authenticated API key attached by Auth, or "" if
// the request was not authenticated. Handlers use it to attribute a request to
// a caller without needing access to the unexported context key.
func KeyFromContext(ctx context.Context) string {
	key, _ := ctx.Value(keyID).(string)
	return key
}
