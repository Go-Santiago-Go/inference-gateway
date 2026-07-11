# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Current state

This repo is **pre-implementation**: it contains planning docs only (`README.md`,
`PROJECT3_BUILD_PLAN.md`, `UI_SPEC.md`, `infer-gateway-ui-mock.html`), no Go or web source, and is
not yet a git repo. `PROJECT3_BUILD_PLAN.md` is the authoritative, phased spec (Phases 0–9); build in
that order. **Phase 7 is the MVP cut line**; everything after is AWS deployment. `UI_SPEC.md` +
`infer-gateway-ui-mock.html` are the behavioral and visual sources of truth for the Phase 7 client.

`CLAUDE.local.md` holds the working style and teaching method for this repo and takes precedence for
*how* to collaborate; this file covers *what* is being built.

## What this is

A containerized Go service in front of AWS Bedrock that adds the production operations layer around
LLM inference (SSE token streaming, per-key API-key auth, per-key rate limiting, retries with
backoff + jitter, and per-request token/cost accounting via structured `slog` logs), plus a React +
TypeScript client (`web/`) that makes each of those features visible in a browser.

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

Docker (the deployed artifact; multi-stage build to distroless):
```bash
docker build -t infer-gateway .
docker run -p 8080:8080 -e AWS_REGION=us-east-1 -e API_KEYS=testkey infer-gateway
```

Web client (`web/`, Vite + React + TS):
```bash
cd web && npm install
npm run dev                # VITE_API_BASE=http://localhost:8080 in web/.env
npm run build              # emits web/dist
```

Smoke-test the stream and the rate limiter:
```bash
curl -N -X POST http://localhost:8080/v1/chat \
  -H "X-API-Key: testkey" -H "Content-Type: application/json" \
  -d '{"prompt":"hi"}'
hey -n 200 -c 20 -H "X-API-Key: testkey" -m POST -d '{"prompt":"hi"}' http://localhost:8080/v1/chat
```

CI (`.github/workflows/ci.yml`) runs `go build`/`go vet`/`go test`; a `web/` build job is added in
Phase 7.

## Architecture (the big picture)

**Everything is a middleware chain, and the handler stays thin.** The request pipeline is
`CORS → auth → rate limit → logging/meter → handler`, each concern wrapping the next. This is the
spine of the service: cross-cutting concerns (auth, limits, cost metering, logging) live in
`internal/middleware`, never inside handlers, so each is testable in isolation and the handler is pure
orchestration.

**The Bedrock client sits behind a Go interface** (`Generator`, in `internal/bedrock`). Handlers
depend on the interface, so they test against a fake with no AWS calls, and models/providers swap
without touching handler code. Retry-with-backoff+jitter logic also lives here, and retries *only*
transient errors (throttling, transient 5xx), never 4xx.

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

Planned Go layout: `cmd/server/` (wires middleware + handler, starts server) · `internal/handler`,
`internal/middleware`, `internal/bedrock`, `internal/meter` · `web/` (client) · `infra/` (Terraform,
Phase 8+).

## Key design decisions to preserve

- **Non-streaming Converse first, then `ConverseStream`.** Get a boring completion working before the
  SSE relay. Verify the current streaming SDK shape against live AWS docs; it changes.
- **In-memory per-key rate limiting** (token bucket via `golang.org/x/time/rate`, one limiter per key
  in a `sync.Map`). Correct for a single ECS task; Redis is the multi-task answer and is a Stretch item.
- **No database in the MVP.** Rate-limit state is in-memory by design.
- **Deploy target is ECS Express Mode on Fargate** (App Runner is closed to new customers). Confirm
  Terraform support for Express Mode before relying on it; fall back to `aws_ecs_service` +
  `aws_ecs_task_definition` if needed, and verify ALB idle-timeout won't cut SSE connections.

Anything past the Phase 7 MVP (batching, OTel tracing, Redis, Prometheus, multi-provider routing) is
on the Stretch list in the build plan; do not build it before the MVP ships.
