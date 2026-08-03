// Command server is the entrypoint for the inference-gateway service.
//
// It configures structured logging, registers the health and readiness
// endpoints, composes the model backends behind a router, wraps the mux in the
// middleware chain, and starts the server. main stays pure wiring: request logic
// lives in handlers, cross-cutting concerns live in internal/middleware, and
// this is the only package that names a concrete backend.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/Go-Santiago-Go/inference-gateway/internal/bedrock"
	"github.com/Go-Santiago-Go/inference-gateway/internal/breaker"
	"github.com/Go-Santiago-Go/inference-gateway/internal/handler"
	"github.com/Go-Santiago-Go/inference-gateway/internal/metrics"
	"github.com/Go-Santiago-Go/inference-gateway/internal/middleware"
	"github.com/Go-Santiago-Go/inference-gateway/internal/ollama"
	"github.com/Go-Santiago-Go/inference-gateway/internal/router"
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

	// Build the generator: Bedrock as primary, optionally Ollama behind it, each
	// wrapped in a circuit breaker and composed by the router. Fail fast if AWS
	// config can't load: a gateway that cannot reach its primary backend must not
	// boot and report healthy.
	bedrockClient, err := bedrock.New(context.Background(), modelID)
	if err != nil {
		log.Fatalf("bedrock client: %v", err)
	}

	// Breaker knobs are config for the same reason the rate-limit knobs are: the
	// right threshold depends on the traffic and the backend's failure profile,
	// and an operator tuning it should not need a rebuild.
	threshold := envInt("BREAKER_THRESHOLD", 5)
	cooldown := time.Duration(envInt("BREAKER_COOLDOWN_SECONDS", 30)) * time.Second

	backends := []router.Backend{{
		Name: "bedrock",
		Gen:  breaker.New("bedrock", bedrockClient, threshold, cooldown),
	}}

	// The fallback is opt-in. With OLLAMA_URL unset the gateway runs single
	// backend exactly as before, so the deployed ECS task needs no Ollama and the
	// local stack gets the full routing demo from one compose file.
	if url := os.Getenv("OLLAMA_URL"); url != "" {
		fallbackModel := envString("OLLAMA_MODEL", "llama3.2")
		backends = append(backends, router.Backend{
			Name: "ollama",
			Gen:  breaker.New("ollama", ollama.New(url, fallbackModel), threshold, cooldown),
		})
		logger.Info("fallback backend enabled", "provider", "ollama", "url", url, "model", fallbackModel)
	}

	// The router satisfies provider.Generator, so the handler takes it exactly as
	// it took the bare Bedrock client. This is the whole payoff of the interface:
	// adding a backend is a change here and nowhere else.
	gen := router.New(backends...)

	// Rate-limit knobs are config, not code, like the keys and model above: the
	// refill rate (requests/sec) and burst size are env-overridable so an operator
	// can tune the cap, or a demo can set a tiny limit to make 429s easy to see,
	// without a rebuild. Defaults suit a single task.
	rps := envFloat("RATE_LIMIT_RPS", 2)
	burst := envInt("RATE_LIMIT_BURST", 5)

	auth := middleware.Auth(apiKeys)
	rateLimit := middleware.RateLimit(rate.Limit(rps), burst)
	chat := handler.New(gen, modelID)
	mux.Handle("POST /v1/chat", auth(rateLimit(http.HandlerFunc(chat.ChatStream))))

	// Allowed browser origins are config, not code: the deployed client's origin is
	// not known until it is hosted, and making this an env var means adding it is a
	// task-definition change rather than an image rebuild. Defaults cover local dev.
	cors := middleware.CORS(envList("CORS_ORIGINS", "http://localhost:5173", "http://127.0.0.1:5173")...)

	// Compose the chain Metrics -> Logging -> CORS -> mux. Named root (not
	// handler) to avoid shadowing the imported handler package. Metrics and
	// Logging are outermost so they wrap every request, including the preflight
	// OPTIONS that CORS short-circuits, and so their latency measurements cover
	// the whole chain rather than the handler alone.
	root := middleware.Metrics(middleware.Logging(cors(mux)))

	// /metrics is registered above the instrumented chain, not inside it. A scrape
	// every few seconds is Prometheus talking to the operator, not a caller
	// consuming the API: it must not need an API key, must not count itself as
	// gateway traffic, and must not emit a log line per scrape. Everything else
	// falls through to root.
	top := http.NewServeMux()
	top.Handle("GET /metrics", metrics.Handler())
	top.Handle("/", root)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", top))
}

// envInt reads an integer env var, returning def when unset or unparseable, so a
// malformed override falls back to a safe default rather than failing to boot.
func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

// envString reads a string env var, returning def when unset or blank.
func envString(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// envFloat reads a float env var, returning def when unset or unparseable.
func envFloat(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return v
	}
	return def
}

// envList reads a comma-separated env var into a slice, returning def when the
// variable is unset or contains no non-blank entries. An empty result would
// silently disable every browser client, so falling back to def is the safer
// failure mode for a malformed override.
func envList(name string, def ...string) []string {
	var out []string
	for v := range strings.SplitSeq(os.Getenv(name), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
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
