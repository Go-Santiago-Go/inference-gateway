# Operations

Running the gateway, watching it work, and what it costs when it does.

## Two data paths, two jobs

**Structured `slog` lines** are per-event and carry caller identity, so they answer "what happened to
request `9f2c`". **Prometheus metrics** are pre-aggregated and carry no identity at all, so they
answer "what is p95 time to first token right now" at a cost that does not grow with traffic.

Answering that second question from logs means parsing and sorting every line in the window, which
gets slowest exactly during an incident. That is the whole argument for having both.

## Running the stack

One command brings up the gateway, an Ollama fallback, a Prometheus that scrapes the gateway, and a
Grafana with the dashboard already loaded. Nothing to click.

```bash
docker compose up -d --build

# One time only: pull the fallback model into the Ollama volume (about 2 GB).
docker compose exec ollama ollama pull llama3.2

open http://localhost:3000   # Grafana, dashboard pre-provisioned, anonymous viewer access
open http://localhost:9090   # Prometheus query UI, useful for debugging an empty panel

docker compose down -v       # tear down, including the metrics and model volumes
```

Provisioning lives in `observability/`: `prometheus.yml` for the scrape config, and
`grafana/provisioning/` plus `grafana/dashboards/inference-gateway.json` for the datasource and
dashboard. Both are files in the repo, so the dashboard is version controlled rather than clicked
together and lost.

## Watching the failover happen

The routing layer is easiest to believe when you break something on purpose. Point the gateway at a
Bedrock model that does not exist and every call to the primary fails:

```bash
BEDROCK_MODEL_ID=does.not.exist docker compose up -d --build gateway

curl -N -X POST localhost:8080/v1/chat \
  -H "X-API-Key: testkey" -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"hi"}]}'
```

The answer still streams, served by Ollama, and the `usage` frame names the model that produced it
with `"cost_usd": 0`, because local inference is free and the meter prices by the model that actually
answered rather than the one configured. In the logs:

```json
{"level":"WARN","msg":"provider failover","served_by":"ollama","failed":"bedrock: ..."}
```

After five consecutive failures the primary's circuit opens, the **Circuit breaker state** panel turns
red, and `gateway_provider_attempts_total{provider="bedrock",outcome="rejected"}` starts climbing:
calls are now being refused in microseconds instead of waiting out the retry budget. Thirty seconds
later exactly one probe is admitted to test recovery.

## The collectors

All defined in `internal/metrics`, registered into the default registry so the Go runtime and process
collectors (goroutines, heap, GC pauses, open file descriptors) come along for free.

| Metric | Type | Labels | What it answers |
|---|---|---|---|
| `gateway_http_requests_total` | counter | `method`, `route`, `status` | Traffic rate, and every failure mode at once: `401` is auth, `429` is the limiter, `502` is the upstream failing after retries |
| `gateway_http_request_duration_seconds` | histogram | `method`, `route` | End-to-end latency including streaming the body, so buckets run to 60s |
| `gateway_time_to_first_token_seconds` | histogram | `model` | What the user actually feels waiting on a streamed answer |
| `gateway_tokens_total` | counter | `model`, `direction` | Token throughput, input and output split because they are priced differently |
| `gateway_cost_usd_total` | counter | `model` | Metered spend; a counter, so `rate(...) * 3600` gives dollars per hour |
| `gateway_generation_duration_seconds` | histogram | `model`, `stream` | Upstream model time only, separating backend latency from gateway overhead |
| `gateway_upstream_errors_total` | counter | `model`, `stream` | Calls that failed after the retry budget was spent, distinct from the `502` rate |
| `gateway_active_streams` | gauge | none | SSE streams open right now; falls when a client disconnects |
| `gateway_provider_attempts_total` | counter | `provider`, `outcome` | Which backend served, and whether a call succeeded, failed, or was refused by an open circuit without being made |
| `gateway_circuit_state` | gauge | `provider` | Circuit breaker state per backend: `0` closed, `1` half-open, `2` open |

## PromQL reference

Paste-ready. Each one backs a panel on the provisioned dashboard.

```promql
# Request rate, broken out by status so 429s and 502s are visible as bands
sum by (status) (rate(gateway_http_requests_total[5m]))

# Rate limiter effectiveness: share of requests rejected
sum(rate(gateway_http_requests_total{status="429"}[5m]))
  / sum(rate(gateway_http_requests_total[5m]))

# p95 time to first token, aggregated correctly across instances
histogram_quantile(0.95,
  sum by (le, model) (rate(gateway_time_to_first_token_seconds_bucket[5m])))

# p50 / p95 / p99 end-to-end latency on the chat route
histogram_quantile(0.99,
  sum by (le) (rate(gateway_http_request_duration_seconds_bucket{route="/v1/chat"}[5m])))

# Spend rate in dollars per hour at current traffic
sum(rate(gateway_cost_usd_total[5m])) * 3600

# Projected monthly spend if current traffic held
sum(rate(gateway_cost_usd_total[1h])) * 3600 * 24 * 30

# Token throughput, input vs output
sum by (direction) (rate(gateway_tokens_total[5m]))

# Mean output tokens per request, a proxy for answer length drift
rate(gateway_tokens_total{direction="output"}[5m])
  / rate(gateway_http_requests_total{route="/v1/chat",status="200"}[5m])

# Upstream error rate after retries are exhausted
sum by (model) (rate(gateway_upstream_errors_total[5m]))

# Which backend is actually serving traffic
sum by (provider, outcome) (rate(gateway_provider_attempts_total[5m]))

# Concurrent SSE streams
gateway_active_streams

# Is the gateway being scraped at all
up{job="inference-gateway"}
```

## Two labels that are deliberately absent

These are the decisions worth defending in a review.

**No API key label anywhere.** Prometheus stores one time series per distinct label combination, so
keying on caller identity grows the footprint with the customer list and never shrinks it, since a
dead series holds index memory until retention expires. Worse, `/metrics` is unauthenticated, so a key
label would write live credentials into a page any scraper can read. Per-key attribution stays in the
`slog` line, where a high-cardinality field costs nothing.

**No raw request path.** `internal/metrics.Route` collapses anything outside a fixed allowlist to
`other`. Labelling with `r.URL.Path` would let a scanner spraying random URLs choose the series count,
which turns a monitoring config into a denial of service vector.

## Performance

The gateway's own serving overhead, measured with Go benchmarks against the fake `Generator`, so no
Bedrock call and no network are involved and the numbers reflect the pipeline's cost rather than the
model's latency. On an 8-core i9-9900K:

| What | Overhead | Under 16-way concurrency |
|---|---|---|
| Full middleware chain (logging → CORS → auth → rate limit → SSE handler → metering) | ~23 µs/request (~43K req/s single-threaded) | ~4.6 µs/request aggregate (~220K req/s) |
| Rate-limit middleware alone | ~223 ns/request, 160 B and 3 allocs | ~182 ns/request aggregate, all load on one key |

The full-chain figure is the gateway's *own* cost, so the takeaway is that the gateway is not the
throughput bottleneck; real throughput is bound by backend latency and concurrency.

The limiter number is worth reading carefully, because it is a smaller claim than it first looks. Both
limiter benchmarks drive a **single** key, so all 16 goroutines share one `rate.Limiter`, and `Allow`
takes that limiter's internal mutex on every call. Per-request cost therefore stays flat under
concurrency (223 ns → 182 ns) rather than collapsing, but it does not scale linearly either: same-key
traffic serializes on that one limiter by design, which is exactly what a per-key budget means. The
`sync.Map` removes contention *between different keys*, and this benchmark does not exercise that, so
the honest claim is "flat under same-key concurrency," not "lock-free."

```bash
# reproduce (no AWS, no cost)
go test -run '^$' -bench . -benchmem ./internal/handler ./internal/middleware
```

## Honest constraints

- **The stack runs locally, not against the deployed ECS task.** Prometheus pulls, so it needs
  reachability to each individual instance. The deployed gateway sits behind a load balancer, and
  scraping that address would round-robin across tasks and sample one arbitrary task's counters as
  though they were the fleet's. The real answers are ECS service discovery or Amazon Managed Service
  for Prometheus. Both are real work that does not change what this demonstrates.
- **`/metrics` is unauthenticated and on the main port.** In production it belongs on a separate admin
  port the load balancer does not expose. One port keeps the Compose wiring simple, and the endpoint
  exposes no caller data precisely because of the cardinality rule above.
- **Percentiles are estimates**, interpolated from histogram buckets rather than computed from sorted
  observations. Accurate enough for dashboards and alerting, and worth saying rather than quoting p95
  as if it were measured directly.
- **Breaker state is per task, not per fleet.** Each task counts its own failures, so with *n* tasks a
  failing provider absorbs up to *n* times the threshold before every circuit is open. Same trade-off
  as the in-memory rate limiter, and it has the same answer: shared state in Redis. Correct and
  defensible for a single task, which is what this deploys.
- **The deployed task runs single backend.** `OLLAMA_URL` is unset on ECS, so Bedrock is the only
  provider there and the router degrades to a list of one. Running Ollama in the cloud means paying
  for a GPU task to sit idle, which buys nothing this project is trying to demonstrate. The full
  routing path runs locally from one `docker compose up`.
