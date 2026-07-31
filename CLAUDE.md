# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Current state

Phases 0–9 are complete and committed: the Go gateway, the React client, CI, the Terraform for ECR
and the IAM roles, and the ECS Express Mode deploy with the client on S3 behind CloudFront.

Work has continued past the original plan, driven by a portfolio review that found the project scoped
below a "gateway". Phase 10 (Prometheus `/metrics` plus a Grafana dashboard, both provisioned from
files and run locally via `docker-compose.yml`) is built and verified against live Bedrock. Phase 11
(multi-provider routing) is built: `Generator` moved to `internal/provider`, Ollama added as a second
backend, `internal/router` composes backends with ordered fallback, and `internal/breaker` adds a
per-backend circuit breaker. `content/observability.md` holds the reasoning behind the metrics work.

`PROJECT3_BUILD_PLAN.md` is the authoritative, phased spec. **Phase 7 is the MVP cut line**;
everything after is AWS deployment. `UI_SPEC.md` + `infer-gateway-ui-mock.html` are the behavioral
and visual sources of truth for the client.

`CLAUDE.local.md` holds the working style and teaching method for this repo and takes precedence for
*how* to collaborate; this file covers *what* is being built.

## What this is

A containerized Go service in front of AWS Bedrock that adds the production operations layer around
LLM inference (SSE token streaming, per-key API-key auth, per-key rate limiting, retries with
backoff + jitter, and per-request token/cost accounting via structured `slog` logs), plus a React +
TypeScript client (`client/`) that makes each of those features visible in a browser.

## Commands (as scaffolded per the build plan)

Go backend:
```bash
go run ./cmd/server        # run the gateway (listens on :8080)
go build ./...
go vet ./...
go test ./...              # all tests
go test ./internal/meter   # a single package
go test -run TestCost ./internal/meter   # a single test
```

Local stack (gateway + Ollama fallback + Prometheus + Grafana, dashboard pre-provisioned):
```bash
docker compose up -d --build   # Grafana :3000, Prometheus :9090, gateway :8080, Ollama :11434
docker compose exec ollama ollama pull llama3.2   # one time, ~2 GB
curl -s localhost:8080/metrics | grep '^gateway_'
docker compose down -v

# Force a failover: point the primary at a model that does not exist.
BEDROCK_MODEL_ID=does.not.exist docker compose up -d --build gateway
```

Docker (the deployed artifact; multi-stage build to distroless):
```bash
docker build -t infer-gateway .
docker run -p 8080:8080 -e AWS_REGION=us-east-1 -e API_KEYS=testkey infer-gateway
```

Web client (`client/`, Vite + React + TS):
```bash
cd client && npm install
npm run dev                # VITE_API_BASE=http://localhost:8080 in client/.env
npm run build              # emits client/dist
```

Smoke-test the stream and the rate limiter:
```bash
curl -N -X POST http://localhost:8080/v1/chat \
  -H "X-API-Key: testkey" -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"hi"}]}'
hey -n 200 -c 20 -H "X-API-Key: testkey" -m POST \
  -d '{"messages":[{"role":"user","content":"hi"}]}' http://localhost:8080/v1/chat
```

CI (`.github/workflows/ci.yml`) runs `go build`/`go vet`/`go test`; a `client/` build job is added in
Phase 7.

## Architecture (the big picture)

**Everything is a middleware chain, and the handler stays thin.** The request pipeline is
`CORS → auth → rate limit → logging/meter → handler`, each concern wrapping the next. This is the
spine of the service: cross-cutting concerns (auth, limits, cost metering, logging) live in
`internal/middleware`, never inside handlers, so each is testable in isolation and the handler is pure
orchestration.

**Every backend sits behind one Go interface** (`Generator`, in `internal/provider`). It lives in its
own package, not with an implementation, because Go satisfies interfaces implicitly, so no backend
needs to import another and only `cmd/server/main.go` names a concrete one. Handlers depend on the
interface, so they test against a fake with no AWS calls. Retry-with-backoff+jitter lives in
`internal/bedrock` and retries *only* transient errors (throttling, transient 5xx), never 4xx.

**The router and the circuit breaker are themselves `Generator`s.** `router.Router` holds an ordered
list of backends and serves the first that succeeds; `breaker.Breaker` wraps one backend and stops
calling it after N consecutive failures, admitting a single probe after a cooldown. Because both
satisfy the same interface they compose without the handler knowing, and adding a provider is a change
in `main` only. Two invariants to preserve: **failover and retry cover the stream open only**, never
mid-completion, because tokens already reached the client and a second generation would contradict
them; and **a cancelled context is never counted as a backend failure**, or user Stops would trip the
breaker on a healthy provider.

**Cost and metrics are attributed to the model that answered**, read from `Completion.Model` /
`Chunk.Model`, not from the handler's configured model. With a router the model is a runtime fact, and
pricing a free local fallback at Bedrock's rate would report spend that never happened.

**Context propagation is load-bearing.** `r.Context()` flows from handler → `Generator` → Bedrock
call. A client disconnect (or the web client's Stop button) cancels the context, which stops the
in-flight Bedrock call and the retry loop instead of burning tokens. Preserve this wiring in any
change to the request path.

**Metering is built in from the first (non-streaming) call, not bolted on.** `internal/meter`
multiplies Converse token counts by a per-model price table to produce `cost_usd`. The streaming path
reuses the same meter and emits a final SSE `event: usage` frame carrying
`{tokens_in, tokens_out, cost_usd, latency_ms}`, the same fields logged as one structured JSON line
per request, and the same fields the client's metrics footer renders.

**Streaming is SSE over plain HTTP, relayed with `http.Flusher`.** `POST /v1/chat` streams Bedrock
`ConverseStream` chunks as `data:` frames, flushing each so it leaves immediately, then the `usage`
frame. SSE (not WebSockets) because the flow is one-directional and works through ALBs and `curl`.
The client reads it with `fetch` + `ReadableStream` (**not** `EventSource`, which can't POST) and an
`AbortController` for Stop.

Go layout: `cmd/server/` (wires middleware, backends, and handler, then starts the server; the only
package naming a concrete backend) · `internal/provider` (the `Generator` interface and its neutral
types) · `internal/bedrock`, `internal/ollama` (backends) · `internal/router`, `internal/breaker`
(both also `Generator`s) · `internal/handler`, `internal/middleware`, `internal/meter`,
`internal/metrics` · `client/` (the web client) · `infra/` (Terraform) · `observability/` (Prometheus
and Grafana provisioning).

## Key design decisions to preserve

- **Non-streaming Converse first, then `ConverseStream`.** Get a boring completion working before the
  SSE relay. Verify the current streaming SDK shape against live AWS docs; it changes.
- **In-memory per-key rate limiting** (token bucket via `golang.org/x/time/rate`, one limiter per key
  in a `sync.Map`). Correct for a single ECS task; Redis is the multi-task answer and is a Stretch item.
- **No database in the MVP.** Rate-limit state is in-memory by design.
- **Deploy target is ECS Express Mode on Fargate** (App Runner is closed to new customers). Confirm
  Terraform support for Express Mode before relying on it; fall back to `aws_ecs_service` +
  `aws_ecs_task_definition` if needed, and verify ALB idle-timeout won't cut SSE connections.

The MVP shipped and deployed, so the Stretch list is now live work rather than a do-not-touch. Two of
its items are done (Prometheus in Phase 10, multi-provider routing in Phase 11). Still parked:
OpenTelemetry tracing, Redis-backed rate limiting for multi-task deploys, request batching, and
semantic response caching.
