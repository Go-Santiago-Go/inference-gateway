// Command server is the entrypoint for the inference-gateway service.
//
// It configures structured logging, registers the health and readiness
// endpoints, wraps the router in the middleware chain, and starts the server.
// main stays pure wiring: request logic lives in handlers, cross-cutting
// concerns live in internal/middleware.
package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/Go-Santiago-Go/inference-gateway/internal/middleware"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok"}`))
	})

	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ready"}`))
	})

	cors := middleware.CORS("http://localhost:5173")

	// Compose the chain Logging -> CORS -> mux. Logging is outermost so it wraps
	// every request, including the preflight OPTIONS that CORS short-circuits, and
	// so its latency measurement covers the whole chain.
	handler := middleware.Logging(cors(mux))

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}
