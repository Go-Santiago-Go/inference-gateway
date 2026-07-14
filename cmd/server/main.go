// Command server is the entrypoint for the inference-gateway service.
//
// It configures structured logging, registers the health and readiness
// endpoints, wraps the router in the middleware chain, and starts the server.
// main stays pure wiring: request logic lives in handlers, cross-cutting
// concerns live in internal/middleware.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/Go-Santiago-Go/inference-gateway/internal/bedrock"
	"github.com/Go-Santiago-Go/inference-gateway/internal/handler"
	"github.com/Go-Santiago-Go/inference-gateway/internal/middleware"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	mux := http.NewServeMux()

	// Model ID is config, not code: env-overridable so the same image can front a
	// different Bedrock model without a rebuild. Region is read separately from
	// AWS_REGION via the AWS config chain inside bedrock.New.
	modelID := os.Getenv("BEDROCK_MODEL_ID")
	if modelID == "" {
		modelID = "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	}

	// API keys are config, not code: a comma-separated API_KEYS list parsed once
	// into a set the Auth middleware checks each request against.
	apiKeys := parseAPIKeys(os.Getenv("API_KEYS"))
	// Fail loud on an empty set: a gateway that authenticates every request but
	// holds no valid keys would 401 all traffic while still reporting healthy,
	// which reads as a silent outage. Refuse to boot instead.
	if len(apiKeys) == 0 {
		log.Fatal("no API keys configured: set API_KEYS to a comma-separated list")
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok"}`))
	})

	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ready"}`))
	})

	// Build the Bedrock-backed generator and the chat handler, then register the
	// endpoint. Fail fast if AWS config can't load: a gateway that cannot reach
	// its backend must not boot and report healthy.
	gen, err := bedrock.New(context.Background(), modelID)
	if err != nil {
		log.Fatalf("bedrock client: %v", err)
	}

	auth := middleware.Auth(apiKeys)
	chat := handler.New(gen, modelID)
	mux.Handle("POST /v1/chat", auth(http.HandlerFunc(chat.Chat)))

	cors := middleware.CORS("http://localhost:5173")

	// Compose the chain Logging -> CORS -> mux. Named root (not handler) to avoid
	// shadowing the imported handler package. Logging is outermost so it wraps
	// every request, including the preflight OPTIONS that CORS short-circuits, and
	// so its latency measurement covers the whole chain.
	root := middleware.Logging(cors(mux))

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", root))
}

// parseAPIKeys turns a comma-separated API_KEYS value into a set of valid keys.
// Surrounding whitespace is trimmed and blank entries are skipped, so a value
// like "a, b," yields the set {a, b}.
func parseAPIKeys(raw string) map[string]bool {
	keys := make(map[string]bool)
	for k := range strings.SplitSeq(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys[k] = true
		}
	}
	return keys
}
