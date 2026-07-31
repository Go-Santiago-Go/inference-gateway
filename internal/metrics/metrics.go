// Package metrics defines the gateway's Prometheus collectors and the handler
// that exposes them. Middleware and handlers call the Record functions here
// instead of touching collectors directly, which keeps every cardinality
// decision (which labels exist, and which deliberately do not) in one file.
//
// No collector in this package is labelled by API key. Prometheus stores one
// time series per distinct combination of label values, so keying on caller
// identity would grow the series count with the customer list and never shrink
// it, and it would copy a credential into an endpoint that is scraped and
// stored without authentication. Per-key attribution stays in the structured
// logs, where a high-cardinality field costs nothing.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Collectors register into the default registry, which already carries the Go
// runtime and process collectors. That gives the dashboard goroutine counts,
// heap size and GC pauses for free alongside the gateway's own series.
var (
	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_http_requests_total",
		Help: "Total HTTP requests served, by method, route and response status.",
	}, []string{"method", "route", "status"})

	// Buckets are stretched well past the client_golang defaults (which top out
	// at 10s) because this latency includes streaming a whole completion, not
	// just producing a response header.
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_http_request_duration_seconds",
		Help:    "End-to-end request latency, including time spent streaming the response body.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"method", "route"})

	tokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_tokens_total",
		Help: "Tokens billed by the upstream model, by model and direction.",
	}, []string{"model", "direction"})

	// A counter, not a gauge: spend only accumulates, and a counter lets a query
	// take a rate over it to get dollars per hour at the current traffic level.
	costUSD = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_cost_usd_total",
		Help: "Cumulative inference spend in US dollars, priced from token counts by internal/meter.",
	}, []string{"model"})

	generationDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_generation_duration_seconds",
		Help:    "Time spent in the upstream model call, excluding gateway overhead.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"model", "stream"})

	// The metric a streaming gateway is actually judged on: total latency says
	// how long the answer took, but time to first token is what the user feels.
	timeToFirstToken = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_time_to_first_token_seconds",
		Help:    "Delay between accepting a streaming request and flushing its first token.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
	}, []string{"model"})

	upstreamErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_upstream_errors_total",
		Help: "Model calls that failed after the retry budget was exhausted.",
	}, []string{"model", "stream"})

	// A gauge, not a counter: streams in flight go up and down, and the useful
	// question is how many are open right now, not how many ever opened.
	activeStreams = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gateway_active_streams",
		Help: "SSE streams currently open.",
	})

	// Labelled by backend rather than by model, because the routing question is
	// which provider served the request. Both label sets stay small and closed:
	// backends are named in main, and outcome is one of three constants.
	providerAttempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_provider_attempts_total",
		Help: "Generation attempts per backend, by outcome: success, error, or rejected by an open circuit.",
	}, []string{"provider", "outcome"})

	// A gauge because a circuit moves between states in both directions. The
	// value is the state's ordinal, which Grafana maps back to a name; encoding
	// state as a label instead would need one series per state per backend to
	// say the same thing.
	circuitState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_circuit_state",
		Help: "Circuit breaker state per backend: 0 closed, 1 half-open, 2 open.",
	}, []string{"provider"})
)

// Provider attempt outcomes. They are constants rather than free strings so the
// label's value set stays closed and a typo cannot mint a new time series.
const (
	OutcomeSuccess  = "success"
	OutcomeError    = "error"
	OutcomeRejected = "rejected"
)

// Handler returns the HTTP handler serving the Prometheus exposition format.
// Mount it outside the auth and logging chain: a scrape every few seconds is
// not caller traffic, so it should neither require an API key nor add a log
// line per scrape.
func Handler() http.Handler {
	return promhttp.Handler()
}

// RecordRequest records one completed HTTP request. The status is a label so
// the 401, 429 and 502 rates are queryable without a separate collector per
// failure mode.
func RecordRequest(method, route string, status int, d time.Duration) {
	requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	requestDuration.WithLabelValues(method, route).Observe(d.Seconds())
}

// RecordGeneration records one successful model call: its token counts, its
// metered cost, and how long the upstream spent producing it.
func RecordGeneration(model string, stream bool, tokensIn, tokensOut int, cost float64, d time.Duration) {
	tokens.WithLabelValues(model, "input").Add(float64(tokensIn))
	tokens.WithLabelValues(model, "output").Add(float64(tokensOut))
	costUSD.WithLabelValues(model).Add(cost)
	generationDuration.WithLabelValues(model, strconv.FormatBool(stream)).Observe(d.Seconds())
}

// RecordFirstToken records how long a streaming request waited before its first
// token reached the client.
func RecordFirstToken(model string, d time.Duration) {
	timeToFirstToken.WithLabelValues(model).Observe(d.Seconds())
}

// RecordUpstreamError counts a model call that failed. It is separate from the
// 502 counted by RecordRequest because a request can fail for gateway reasons
// (a bad body, an empty bucket) that never reached the model at all.
func RecordUpstreamError(model string, stream bool) {
	upstreamErrors.WithLabelValues(model, strconv.FormatBool(stream)).Inc()
}

// StreamStarted marks an SSE stream as open. Every call must be paired with
// StreamEnded, or the gauge drifts upward and never recovers.
func StreamStarted() {
	activeStreams.Inc()
}

// StreamEnded marks an SSE stream as closed.
func StreamEnded() {
	activeStreams.Dec()
}

// RecordProviderAttempt counts one call against a backend. Outcome must be one
// of the Outcome constants. A rejected attempt never reached the backend, which
// is what distinguishes a tripped circuit from an upstream failure on a graph.
func RecordProviderAttempt(provider, outcome string) {
	providerAttempts.WithLabelValues(provider, outcome).Inc()
}

// SetCircuitState records a backend's circuit breaker state, where the value is
// the State's ordinal. Called on transition rather than polled, so the gauge is
// accurate between scrapes instead of only at them.
func SetCircuitState(provider string, state int) {
	circuitState.WithLabelValues(provider).Set(float64(state))
}

// routes is the fixed set of paths the gateway serves. Anything else collapses
// to a single "other" series in Route.
var routes = map[string]bool{
	"/health":  true,
	"/ready":   true,
	"/v1/chat": true,
}

// Route normalizes a request path onto the fixed set of routes the gateway
// serves, mapping anything unrecognized to "other". Labelling with the raw
// r.URL.Path instead would let a scanner spraying random URLs create unbounded
// series and exhaust the scrape target's memory, which is why the allowlist is
// a correctness requirement rather than tidiness.
func Route(path string) string {
	if routes[path] {
		return path
	}
	return "other"
}
