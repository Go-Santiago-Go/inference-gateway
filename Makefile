# Task runner. The verbs (help, up, down, run, test, lint, deploy, destroy) are
# the same in every repo in this portfolio, so a reviewer who has seen one knows
# what to type here regardless of the language underneath. Targets prefixed
# client- are this repo's addition, since the gateway ships a browser client.

# Load a local .env if present, so `make run` picks up AWS_REGION and API_KEYS
# without the caller having to export anything. `-include` keeps this optional,
# and .env is gitignored.
-include .env
export

.DEFAULT_GOAL := help

.PHONY: help run build test lint bench up down ollama-pull \
        client client-test docker-build bootstrap deploy url client-deploy destroy

help: ## List the available targets
	@grep -hE '^[a-z][a-zA-Z0-9_-]*:.*?## ' $(MAKEFILE_LIST) \
	  | awk -F':.*?## ' '{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# --- Local -------------------------------------------------------------------

run: ## Run the gateway on :8080 (needs AWS_REGION and API_KEYS)
	go run ./cmd/server

build: ## Compile everything
	go build ./...

test: ## Run the Go test suite (no cloud access needed)
	go test ./...

lint: ## Vet and formatting check, matching CI
	go vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
	  echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi

bench: ## Benchmark the handler and middleware (fake Generator, so no AWS, no cost)
	go test -run '^$$' -bench . -benchmem ./internal/handler ./internal/middleware

up: ## Start the full stack: gateway, Ollama, Prometheus, Grafana
	docker compose up -d --build

down: ## Stop the stack (add VOLUMES=1 to also drop Prometheus and Ollama data)
	docker compose down $(if $(VOLUMES),-v,)

ollama-pull: ## Pull the fallback model into the running Ollama container (one time, ~2 GB)
	docker compose exec ollama ollama pull llama3.2

# --- Client ------------------------------------------------------------------
# Kept separate from the Go targets so `make test` stays fast and credential
# free; CI runs both as independent jobs.

client: ## Run the client dev server against VITE_API_BASE (default localhost:8080)
	cd client && npm install && npm run dev

client-test: ## Type-check, build, and test the client, matching CI
	cd client && npm ci && npm run build && npm test

# --- Cloud -------------------------------------------------------------------
# Two stacks split by lifetime: bootstrap is free and stays up, infra bills by
# the hour and is destroyed after each session.

docker-build: ## Build the distroless container image
	docker build -t inference-gateway:local .

bootstrap: ## Apply the persistent stack (ECR repo + CI role). Run once.
	terraform -chdir=infra/bootstrap init
	terraform -chdir=infra/bootstrap apply

deploy: ## Apply the billable app stack (ECS Express, S3, CloudFront)
	terraform -chdir=infra init
	terraform -chdir=infra apply

url: ## Print the live gateway and client URLs
	@echo "gateway: $$(terraform -chdir=infra output -raw gateway_url)"
	@echo "client:  $$(terraform -chdir=infra output -raw client_url)"

# The gateway URL is compiled into the bundle at build time, so this has to run
# after `make deploy`. CloudFront caches aggressively; the invalidation is what
# makes a re-upload visible immediately rather than at TTL expiry.
client-deploy: ## Build the client against the live gateway and upload it to S3
	@GATEWAY=$$(terraform -chdir=infra output -raw gateway_url); \
	BUCKET=$$(terraform -chdir=infra output -raw client_bucket); \
	DIST=$$(terraform -chdir=infra output -raw client_distribution_id); \
	cd client && npm ci && VITE_API_BASE="$$GATEWAY" npm run build && cd .. && \
	aws s3 sync client/dist "s3://$$BUCKET" --delete && \
	aws cloudfront create-invalidation --distribution-id "$$DIST" --paths '/*'

destroy: ## Tear the billable stack down. Run this after every session.
	terraform -chdir=infra destroy
