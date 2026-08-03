# Local development

The fastest path is local. The Go service runs natively and talks to Bedrock, so you need no local
database and no container to see the full request path. It does call Bedrock, so the machine running
it needs AWS credentials with **Bedrock access** and a Converse-stream model enabled in the region.

**Prerequisites.** [Go 1.26+](https://go.dev/doc/install),
[Docker](https://docs.docker.com/get-docker/), and AWS credentials configured (`aws configure`) with
[model access](https://docs.aws.amazon.com/bedrock/latest/userguide/model-access.html) enabled for a
Claude model in your region.

## Run the gateway

```bash
git clone https://github.com/Go-Santiago-Go/inference-gateway.git
cd inference-gateway

export AWS_REGION=us-east-1
export API_KEYS=testkey        # the server refuses to boot without at least one key

go run ./cmd/server             # listens on :8080
```

In another terminal, stream a completion. `-N` disables curl buffering so tokens print as they
arrive:

```bash
curl -N -X POST localhost:8080/v1/chat \
  -H "X-API-Key: testkey" -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"say hello in five words"}]}'
# data: Hello,
# data:  how are you today
# data: ?
# event: usage
# data: {"tokens_in":14,"tokens_out":10,"cost_usd":0.000064,"latency_ms":1667,"model":"us.anthropic.claude-haiku-4-5-20251001-v1:0"}
```

## Configuration

The listen port is fixed at `:8080`. Everything else is environment driven.

| Variable | Default | Purpose |
|---|---|---|
| `API_KEYS` | none, **required** | Comma-separated list of valid keys. The server refuses to boot without one. |
| `AWS_REGION` | from the AWS SDK chain | Region where Bedrock model access is enabled |
| `BEDROCK_MODEL_ID` | `us.anthropic.claude-haiku-4-5-20251001-v1:0` | Primary model. Config, not code, so one image can front several models. |
| `RATE_LIMIT_RPS` | `2` | Per-key token-bucket refill rate, requests/second |
| `RATE_LIMIT_BURST` | `5` | Per-key bucket size |
| `CORS_ORIGINS` | `http://localhost:5173,http://127.0.0.1:5173` | Comma-separated browser origin allowlist |
| `OLLAMA_URL` | unset | Enables the fallback backend. Unset means the gateway runs single backend. |
| `OLLAMA_MODEL` | `llama3.2` | Model to request from Ollama |
| `BREAKER_THRESHOLD` | `5` | Consecutive failures before a backend's circuit opens |
| `BREAKER_COOLDOWN_SECONDS` | `30` | Wait before admitting a half-open probe |

Fallback routing is opt-in. Leave `OLLAMA_URL` unset and `router.Router` degrades to a list of one.

## Run the web client

With the gateway running on `:8080`, start the client so you can watch the stream, cancel it, and see
per-request and cumulative cost in the browser:

```bash
cd client
npm install
npm run dev     # http://localhost:5173 · reads VITE_API_BASE from client/.env
npm run build   # type-checks and emits client/dist
npm test        # SSE frame-parser unit tests (vitest)
```

## Run the deployed artifact

The same distroless container that ships to ECS:

```bash
docker build -t inference-gateway .
docker run -p 8080:8080 -e AWS_REGION=us-east-1 -e API_KEYS=testkey inference-gateway
```

## The full local stack

One command brings up the gateway, an Ollama fallback backend, a Prometheus that scrapes the gateway,
and a Grafana with the dashboard already loaded. Nothing to click. See
[OPERATIONS.md](OPERATIONS.md) for what to do with it.

```bash
docker compose up -d --build

# One time only: pull the fallback model into the Ollama volume (about 2 GB).
docker compose exec ollama ollama pull llama3.2

open http://localhost:3000   # Grafana, dashboard pre-provisioned, anonymous viewer access
open http://localhost:9090   # Prometheus query UI, useful for debugging an empty panel

docker compose down -v       # tear down, including the metrics and model volumes
```

The gateway container reads AWS credentials from a read-only mount of `~/.aws`, so it calls real
Bedrock. Compose sets a deliberately tight `RATE_LIMIT_RPS=2` so a handful of concurrent requests trip
the limiter and the `429` panel has something to show without a load generator.

## Development commands

```bash
go build ./...            # build everything
go vet ./...              # static checks (also runs in CI)
go test ./...             # all tests (also runs in CI)
go test ./internal/meter  # a single package
go test -run TestCost ./internal/meter

# benchmarks: no AWS, no cost, they run against the fake Generator
go test -run '^$' -bench . -benchmem ./internal/handler ./internal/middleware
```

CI (`.github/workflows/ci.yml`) runs two jobs on every push and pull request: `go build`/`go
vet`/`go test` for the service, and `npm ci`/`npm run build`/`npm test` for the client, so a broken
frontend fails the pipeline too.

## Load testing

```bash
hey -n 200 -c 20 -H "X-API-Key: testkey" -m POST \
  -d '{"messages":[{"role":"user","content":"hi"}]}' http://localhost:8080/v1/chat
```
