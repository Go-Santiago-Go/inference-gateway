# API reference

One streaming endpoint, two probes, and a metrics page.

| Endpoint | Auth | Purpose |
|---|---|---|
| `POST /v1/chat` | `X-API-Key` | Stream a completion over SSE, ending with a `usage` event |
| `GET /health` | none | Liveness probe |
| `GET /ready` | none | Readiness probe |
| `GET /metrics` | none | Prometheus exposition format |

## `POST /v1/chat`

Streams a completion back token by token over Server-Sent Events. The request is authenticated by the
`X-API-Key` header against the set loaded from `API_KEYS`; an unknown or missing key is rejected with
`401` in middleware, before any backend call. The handler relays the backend's events as `data:`
frames, flushing each immediately, then emits a final `event: usage` frame carrying the request's
token counts, cost, latency, and the model that served it, the same fields logged as one structured
JSON line.

The `model` field is not decoration: with a router in front, which backend answered is a runtime fact,
and it is what the cost was priced from.

```bash
curl -N -X POST localhost:8080/v1/chat \
  -H "X-API-Key: testkey" -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"Explain a token bucket rate limiter in two sentences."}]}'
# data: A token bucket ...
# ...
# event: usage
# data: {"tokens_in":18,"tokens_out":64,"cost_usd":0.0021,"latency_ms":840,"model":"us.anthropic.claude-haiku-4-5-20251001-v1:0"}
```

### Request

```json
{ "messages": [{ "role": "user" | "assistant", "content": "string" }] }
```

The gateway is stateless, so a multi-turn conversation resends the full history each turn and the
final message must be the user's.

### Status codes

| Code | Meaning |
|---|---|
| `200` | Stream opened; tokens follow as `data:` frames, then `event: usage` |
| `400` | Malformed body, empty history, or a non-user final turn |
| `401` | Missing or unknown `X-API-Key`, rejected in middleware |
| `429` | Key over its rate limit, with a `Retry-After` header |
| `502` | Upstream failed after retries (the backend failed, not the gateway) |

The request context threads into the SDK call, so a client disconnect cancels the upstream request
instead of paying for unread tokens.

### Cost accounting

Each Converse response carries input and output token counts. The meter multiplies those by a
per-model price table to compute `cost_usd` per request, which is both returned in the `usage` event
and logged, so spend is attributable per caller:

```json
{"request_id":"...","key":"testkey","model":"...","tokens_in":18,"tokens_out":64,"cost_usd":0.0021,"latency_ms":840}
```

### Rate limiting

Each API key gets its own token-bucket limiter (`golang.org/x/time/rate`, one limiter per key in a
`sync.Map`), so a burst is absorbed up to the bucket size and then requests settle to the sustained
refill rate. A key whose bucket is empty is rejected with `429 Too Many Requests` and a `Retry-After`
header, in middleware, before the request reaches Bedrock.

Firing 100 concurrent requests at a single key with a demo-tuned burst of 5 shows the limiter engaging
at the burst size:

```
94 429   ← rejected in middleware, never reached Bedrock
 6 200   ← served
```

Six rather than five get through because the bucket refills at 2/second while the burst is still
arriving, so the exact split moves by one or two with timing; the shape is what matters.

Every rejected request logs `latency_ms: 0` (all 94, verified in the container logs) because it
short-circuits before the upstream call. Rejecting there is what makes the limiter a cost control at
all: a `429` costs nothing, because no upstream call was ever made.

**What it does not do is cap spend.** The bucket meters requests per second, and cost is driven by
tokens per second, which is a different quantity. At the default 2 req/s a caller sending small
prompts lands near $11/day against Haiku 4.5 pricing, but the same 2 req/s sending 100K-token prompts
is over $20,000/day. Request rate bounds cost only when request size is also bounded, and nothing here
bounds request size.

A real spending cap needs a token-aware limiter: a second bucket metered in tokens per minute,
debited by the actual usage the meter already computes per request. The accounting side of that is
built, since every response is already priced by the model that answered; the enforcement side is
not. See the known gaps in the README.

The burst and rate are operational knobs; the demo value is deliberately low to make the behavior
visible and the load test near-free.

### Retries

Bedrock calls are wrapped in a retry loop that fires only on *transient* failures, the ones where
re-sending the identical request could plausibly succeed: `ThrottlingException`,
`ServiceUnavailableException`, `InternalServerException`, and `ModelTimeoutException`. Client errors
(validation, auth, bad model ID) are never retried, because the identical request fails identically
forever, so a retry only adds latency to the same error. Classification is by error *type* via
`errors.As`, with a `smithy.APIError` code fallback for untyped errors, and an unrecognized error
defaults to non-retryable: retry only on positive evidence.

The schedule is exponential backoff plus jitter, capped at 3 attempts (1 original + 2 retries):

```
attempt 1 ─── 1s + jitter ─── attempt 2 ─── 2s + jitter ─── attempt 3
```

Backoff and jitter do different jobs and the design needs both. **Backoff is escalation**: each
failure doubles the wait, so a struggling Bedrock gets progressively more room instead of being
hammered by the retries themselves. **Jitter is desynchronization**: without a random nudge every
throttled client computes the identical 1.000s and 2.000s and fires again simultaneously, so the
thundering herd reforms on every round. Jitter smears them across a window (`[0, 250ms)` here) so
Bedrock sees a trickle instead of a wall.

**The wait is cancellable.** The backoff sleeps on a `select` over `time.After` and `ctx.Done()`
rather than `time.Sleep`, which is uncancellable. A client that disconnects mid-backoff cancels the
request context, the loop abandons the wait and returns immediately, and Bedrock is never called
again. Without that, a disconnect during backoff is invisible until the sleep completes, and the
gateway pays for a completion nobody will read.

Two deliberate choices worth naming. The AWS SDK **retries by default** (`retry.Standard`, 3
attempts), so it is explicitly disabled with `config.WithRetryMaxAttempts(1)`; left on, the two
retryers nest and one logical request can hit Bedrock up to 9 times on two stacked backoff schedules.
And for streaming, **only the stream open is retried, never mid-stream**: once deltas are flowing the
client already holds tokens and Bedrock cannot resume mid-completion, so a retry would regenerate from
scratch and duplicate or contradict what was already sent.

## `GET /health` · `GET /ready`

Liveness and readiness probes for the load balancer and orchestrator. Both are open, outside the auth
middleware, so they never require an API key.

```bash
curl localhost:8080/health   # {"status":"ok"}
```

## `GET /metrics`

The Prometheus exposition format: the gateway's own collectors plus the Go runtime and process
collectors. It is registered **above** the instrumented middleware chain rather than inside it,
because a scrape every few seconds is Prometheus talking to the operator, not a caller consuming the
API. It therefore needs no API key, does not count itself as gateway traffic, and does not emit a log
line per scrape.

```bash
curl -s localhost:8080/metrics | grep '^gateway_'
```

```
gateway_active_streams 0
gateway_cost_usd_total{model="us.anthropic.claude-haiku-4-5-20251001-v1:0"} 6.3e-05
gateway_http_requests_total{method="GET",route="/health",status="200"} 4
gateway_http_requests_total{method="GET",route="other",status="404"} 1
gateway_http_requests_total{method="POST",route="/v1/chat",status="200"} 1
gateway_http_requests_total{method="POST",route="/v1/chat",status="401"} 1
gateway_tokens_total{direction="input",model="us.anthropic.claude-haiku-4-5-20251001-v1:0"} 13
gateway_tokens_total{direction="output",model="us.anthropic.claude-haiku-4-5-20251001-v1:0"} 10
```

That `route="other"` line is a request for `/wp-login.php`. Unrecognized paths collapse into a single
series on purpose; see [OPERATIONS.md](OPERATIONS.md) for the cardinality reasoning.
