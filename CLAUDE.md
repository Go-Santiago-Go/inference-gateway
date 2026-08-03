# CLAUDE.md

How to get this repository running and how to verify a change. Everything else lives in `docs/`; see
the map at the bottom.

`inference-gateway` is a containerized Go service that puts the production operations layer in front
of LLM inference: SSE token streaming, per-key auth, per-key rate limiting, retries with backoff and
jitter, multi-provider routing with per-backend circuit breakers, and per-request token and cost
metering. A React and TypeScript client in `client/` exercises all of it in a browser.

## Run it

Needs AWS credentials with Bedrock model access for a Claude model in your region. No database, no
container required.

```bash
export AWS_REGION=us-east-1
export API_KEYS=testkey        # refuses to boot without at least one key
go run ./cmd/server            # listens on :8080
```

```bash
curl -N -X POST localhost:8080/v1/chat \
  -H "X-API-Key: testkey" -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"hi"}]}'
```

The full stack, adding the fallback backend, Prometheus, and Grafana with the dashboard already
provisioned:

```bash
docker compose up -d --build   # Grafana :3000, Prometheus :9090, gateway :8080, Ollama :11434
docker compose exec ollama ollama pull llama3.2   # one time, about 2 GB
docker compose down -v
```

The web client:

```bash
cd client && npm install
npm run dev     # reads VITE_API_BASE from client/.env, default http://localhost:8080
```

## Verify a change

What CI runs, and what should pass before any commit:

```bash
go build ./... && go vet ./... && go test ./...
cd client && npm ci && npm run build && npm test
```

Narrower loops while working:

```bash
go test ./internal/meter                 # one package
go test -run TestCost ./internal/meter   # one test

# benchmarks: no AWS, no cost, they run against a fake Generator
go test -run '^$' -bench . -benchmem ./internal/handler ./internal/middleware
```

The deployed artifact is a multi-stage build to distroless:

```bash
docker build -t inference-gateway .
docker run -p 8080:8080 -e AWS_REGION=us-east-1 -e API_KEYS=testkey inference-gateway
```

## Things that will waste your time

- **`API_KEYS` is required.** The server exits at startup without it rather than serving an open
  endpoint.
- **Failover needs `OLLAMA_URL`.** Unset, the router is a list of one, and nothing warns you. This is
  also why the deployed ECS task does not fail over.
- **Force a failover on purpose** by pointing the primary at a model that does not exist:
  `BEDROCK_MODEL_ID=does.not.exist docker compose up -d --build gateway`.
- **Benchmarks cost nothing.** They run against a fake `Generator`, so no AWS credentials and no
  Bedrock calls. Run them freely.
- **Load-test with `hey`** to see the limiter reject traffic:
  `hey -n 200 -c 20 -H "X-API-Key: testkey" -m POST -d '{"messages":[{"role":"user","content":"hi"}]}' http://localhost:8080/v1/chat`
- **Tear down billable AWS resources after each session.** `infra/` is the billable stack and gets
  destroyed; `infra/bootstrap/` is free and stays up.
- **Before writing docs, read `docs/CONVENTIONS.md`.** It carries the accuracy guards, and each one is
  there because it was gotten wrong.

## Where everything is

| Doc | Scope |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Middleware chain and its ordering, `Generator` composition, failover and breaker boundaries, the web client |
| [docs/API.md](docs/API.md) | Endpoint reference, status codes, cost accounting, rate limiter behavior, retry classification and schedule |
| [docs/LOCAL_DEV.md](docs/LOCAL_DEV.md) | Full environment variable reference, the client, the Compose stack, tests and benchmarks |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | What gets provisioned on AWS, both Terraform stacks, step by step deploy, teardown, troubleshooting |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Collectors, PromQL reference, forcing a failover, benchmarks, cardinality decisions |
| [docs/CONVENTIONS.md](docs/CONVENTIONS.md) | Documentation rules, the README spine, accuracy guards, generated artifacts |

| Path | Contents |
|---|---|
| `cmd/server/` | Wires middleware, backends, and handler. The only package naming a concrete backend. |
| `internal/provider/` | The `Generator` interface and its neutral types. No implementations. |
| `internal/bedrock/`, `internal/ollama/` | The two backends. Retry with backoff and jitter lives in `bedrock`. |
| `internal/router/`, `internal/breaker/` | Ordered fallback and per-backend circuit breaking. Both are `Generator`s. |
| `internal/handler/` | The `/v1/chat` SSE relay and the probes. |
| `internal/middleware/` | CORS, auth, rate limiting, logging, metrics. |
| `internal/meter/` | Token counts times a per-model price table. |
| `internal/metrics/` | Prometheus collectors and the route-label allowlist. |
| `client/` | React + TypeScript (Vite) client. |
| `infra/` | Terraform. `infra/bootstrap/` is free and persistent; `infra/` is billable. |
| `observability/` | Prometheus scrape config, provisioned Grafana datasource and dashboard. |
