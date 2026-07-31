package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Collectors live in the default registry and persist across tests in this
// package, so every assertion here is a delta around the call under test rather
// than an absolute value.

func TestRouteCollapsesUnknownPaths(t *testing.T) {
	// The cardinality guard: known routes survive as labels, everything else
	// becomes one shared series so a caller cannot mint series at will.
	for _, path := range []string{"/health", "/ready", "/v1/chat"} {
		if got := Route(path); got != path {
			t.Errorf("Route(%q) = %q, want %q", path, got, path)
		}
	}
	for _, path := range []string{"/wp-login.php", "/../etc/passwd", "/v1/chat/extra", ""} {
		if got := Route(path); got != "other" {
			t.Errorf("Route(%q) = %q, want %q", path, got, "other")
		}
	}
}

func TestRecordRequestCountsByStatus(t *testing.T) {
	// A 429 and a 200 on the same route must land in separate series, since the
	// rate-limiter dashboard is built entirely on splitting by status.
	ok := requests.WithLabelValues("POST", "/v1/chat", "200")
	limited := requests.WithLabelValues("POST", "/v1/chat", "429")

	beforeOK := testutil.ToFloat64(ok)
	beforeLimited := testutil.ToFloat64(limited)

	RecordRequest("POST", "/v1/chat", http.StatusOK, 250*time.Millisecond)
	RecordRequest("POST", "/v1/chat", http.StatusTooManyRequests, time.Millisecond)
	RecordRequest("POST", "/v1/chat", http.StatusTooManyRequests, time.Millisecond)

	if got := testutil.ToFloat64(ok) - beforeOK; got != 1 {
		t.Errorf("200 counter rose by %v, want 1", got)
	}
	if got := testutil.ToFloat64(limited) - beforeLimited; got != 2 {
		t.Errorf("429 counter rose by %v, want 2", got)
	}
}

func TestRecordGenerationAccumulatesTokensAndCost(t *testing.T) {
	const model = "test-model"

	in := tokens.WithLabelValues(model, "input")
	out := tokens.WithLabelValues(model, "output")
	spend := costUSD.WithLabelValues(model)

	beforeIn := testutil.ToFloat64(in)
	beforeOut := testutil.ToFloat64(out)
	beforeSpend := testutil.ToFloat64(spend)

	RecordGeneration(model, true, 100, 250, 0.0015, 900*time.Millisecond)
	RecordGeneration(model, false, 20, 50, 0.0003, 400*time.Millisecond)

	if got := testutil.ToFloat64(in) - beforeIn; got != 120 {
		t.Errorf("input tokens rose by %v, want 120", got)
	}
	if got := testutil.ToFloat64(out) - beforeOut; got != 300 {
		t.Errorf("output tokens rose by %v, want 300", got)
	}
	// Float accumulation, so compare within a tolerance well below a cent.
	if got := testutil.ToFloat64(spend) - beforeSpend; got < 0.00179 || got > 0.00181 {
		t.Errorf("cost rose by %v, want ~0.0018", got)
	}
}

func TestStreamGaugeReturnsToZero(t *testing.T) {
	// The gauge only stays trustworthy if every StreamStarted is paired, which is
	// what the deferred StreamEnded in the stream handler guarantees.
	before := testutil.ToFloat64(activeStreams)

	StreamStarted()
	StreamStarted()
	if got := testutil.ToFloat64(activeStreams) - before; got != 2 {
		t.Fatalf("gauge rose by %v with two streams open, want 2", got)
	}

	StreamEnded()
	StreamEnded()
	if got := testutil.ToFloat64(activeStreams) - before; got != 0 {
		t.Errorf("gauge settled at +%v after both streams closed, want 0", got)
	}
}

func TestHandlerExposesEveryGatewayCollector(t *testing.T) {
	// Registration is easy to get wrong silently: a collector that is declared but
	// never scraped looks fine in code and produces an empty dashboard panel. This
	// asserts against the real exposition output the way Prometheus reads it.
	RecordRequest("POST", "/v1/chat", http.StatusOK, time.Second)
	RecordGeneration("test-model", true, 1, 1, 0.0001, time.Second)
	RecordFirstToken("test-model", 300*time.Millisecond)
	RecordUpstreamError("test-model", true)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	want := []string{
		"gateway_http_requests_total",
		"gateway_http_request_duration_seconds_bucket",
		"gateway_tokens_total",
		"gateway_cost_usd_total",
		"gateway_generation_duration_seconds_bucket",
		"gateway_time_to_first_token_seconds_bucket",
		"gateway_upstream_errors_total",
		"gateway_active_streams",
		// Free from registering into the default registry, and worth asserting so
		// a future switch to a custom registry does not silently drop them.
		"go_goroutines",
	}
	for _, name := range want {
		if !strings.Contains(body, name) {
			t.Errorf("scrape output is missing %q", name)
		}
	}

	// The deliberate omission from the package doc: no collector may carry caller
	// identity, both because it is unbounded and because /metrics is unauthenticated.
	if strings.Contains(body, "api_key=") {
		t.Error("scrape output exposes an api_key label, which leaks credentials and explodes cardinality")
	}
}
