# Conventions

Rules for changing this repository's documentation and for the claims it makes. Code conventions are
the Go defaults plus `go vet`; these are the ones a linter cannot enforce and that have been gotten
wrong before.

## Accuracy guards

Claims in the docs must match the code. These three are easy to get wrong and each has been wrong at
some point.

**There is no spending cap, and the docs must not claim one.** The token bucket meters *requests per
second*. Cost is driven by *tokens per second*. A rate bounds spend only when request size is also
bounded, and nothing here bounds request size. Two requests per second of small prompts is a few
dollars a day; the same rate with 100K-token prompts is four figures. A real cap needs a second bucket
metered in tokens per minute, debited by the usage `internal/meter` already computes. The accounting
half exists; the enforcement half does not. Do not describe rate limiting as cost control.

**Do not put derived figures in the "Measured" table** in the README's `## Results` section. That
table is for numbers produced by `go test -bench`. Arithmetic estimates belong in prose, labelled as
estimates.

**Failover is conditional.** It requires `OLLAMA_URL` to be set. The deployed ECS task does not set
it, so the router there is a list of one. Any sentence claiming the deployed service fails over is
false.

## Generated artifacts

`docs/demo.gif` is **generated, not recorded by hand.** A script drives the real gateway through the
real client and captures the result, so every number visible in it is that run's own output.

- Never hand edit it.
- Never describe it with numbers it does not show. The token counts, cost, latency, and `Retry-After`
  in the README's alt text and caption come from the run that produced the current file.
- A change to the client's markup or theme makes it stale, and nothing in the tree will say so.
- The generator is local only and not committed. If it is unavailable, flag the gif as stale rather
  than editing the caption to match a UI the image no longer depicts.

## Documentation layout

The docs are split by audience. **`README.md` is the overview and stays short.** Depth lives here:

| File | Scope |
|---|---|
| `docs/ARCHITECTURE.md` | Middleware chain, `Generator` composition, failover and breaker boundaries, the client's mapping to backend capabilities |
| `docs/API.md` | Endpoint reference, status codes, cost accounting, rate limiter behavior, retry classification and schedule |
| `docs/LOCAL_DEV.md` | Local run, full environment variable reference, client, Compose stack, tests and benchmarks |
| `docs/DEPLOYMENT.md` | What gets provisioned on AWS, both Terraform stacks, step by step deploy, teardown, troubleshooting |
| `docs/OPERATIONS.md` | Collectors, PromQL reference, forcing a failover, benchmarks, cardinality decisions |
| `docs/CONVENTIONS.md` | This file |

**When a README section grows past a few paragraphs, move it into the matching file above and leave a
link.** Do not let the README reabsorb depth.

## The README spine

`README.md` follows a fixed section order, shared across the portfolio repos so they read as one body
of work. Do not rename or reorder these, and do not insert new top-level sections between them:

```
title + badges + what it is → Demo → Contents → The problem → How it works → Quickstart
→ Trade-offs → Results → What I'd do differently → Known gaps and next steps
→ Repo layout → Documentation → License
```

Two rules specific to that spine:

- **`Results` ships only if it contains a number a reader could reproduce with a command.** Estimates
  and derived arithmetic are prose, not table rows. No number means no section.
- **`What I'd do differently` is hindsight; `Known gaps and next steps` is scope.** They are different
  claims and must not be merged. Folding a deliberate scoping call into the hindsight section turns a
  defensible decision into an apparent regret.

## Writing rules

- **Never link the README or `docs/` to anything in `content/`.** That directory is gitignored, so
  those links 404 for anyone reading the repo on GitHub. `content/` holds unpublished drafts only and
  is never staged or committed.
- **Keep diagrams single-direction with no back edges.** Mermaid `flowchart LR` renders as spaghetti
  once an edge points backwards. If a diagram needs to show two concerns, make it two diagrams. No
  `classDef` or `style` blocks; they add noise without adding information.
- **The name is `inference-gateway` everywhere.** Repo, module, image tag, Compose project, Grafana
  dashboard uid and title, doc titles. `infer-gateway` is dead and should not reappear.
- **Verify fast-moving SDK and service shapes against live documentation** rather than memory, then
  write what you verified. The Bedrock `ConverseStream` response shape and ECS Express Mode's
  Terraform support have both changed under this project.
