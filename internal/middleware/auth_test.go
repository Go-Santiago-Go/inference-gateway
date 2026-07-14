package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuth(t *testing.T) {
	keys := map[string]bool{"good-key": true}

	tests := []struct {
		name       string
		header     string // value sent in X-API-Key; "" means no header
		wantStatus int
		wantNext   bool // should the request reach the wrapped handler?
	}{
		{name: "valid key passes through", header: "good-key", wantStatus: http.StatusOK, wantNext: true},
		{name: "unknown key is rejected", header: "bad-key", wantStatus: http.StatusUnauthorized, wantNext: false},
		{name: "missing key is rejected", header: "", wantStatus: http.StatusUnauthorized, wantNext: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// nextCalled records whether Auth passed the request through. The
			// handler also asserts the key made it into the context.
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				if got := KeyFromContext(r.Context()); got != tt.header {
					t.Errorf("key in context = %q, want %q", got, tt.header)
				}
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
			if tt.header != "" {
				req.Header.Set("X-API-Key", tt.header)
			}
			rec := httptest.NewRecorder()

			Auth(keys)(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if nextCalled != tt.wantNext {
				t.Errorf("next called = %v, want %v", nextCalled, tt.wantNext)
			}
		})
	}
}
