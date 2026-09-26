.PHONY: help up down logs seed crawl reindex rankeval rankeval-semantic datagen bench test test-integration lint build dev-api dev-web

CRAWL_MIN_STARS ?= 1000
CRAWL_MAX ?= 10000

help: ## Show targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  %-10s %s\n", $$1, $$2}'

up: ## Start the full stack (http://localhost:3000)
	docker compose up -d --build

down: ## Stop the stack
	docker compose down

logs: ## Tail logs
	docker compose logs -f api web

seed: ## Load the bundled sample repositories
	docker compose run --rm crawler -seed testdata/seed_repositories.json

crawl: ## Crawl GitHub (set GITHUB_TOKEN in .env)
	docker compose run --rm crawler -min-stars $(CRAWL_MIN_STARS) -max $(CRAWL_MAX)

reindex: ## Rebuild the index from PostgreSQL and swap the alias
	docker compose run --rm crawler -reindex

rankeval-semantic: ## Evaluate semantic and hybrid configurations (needs make up && make seed)
	EMBEDDINGS_URL=http://localhost:8000 go run ./cmd/rankeval -semantic-grid -pool

rankeval: ## Measure relevance against testdata/judgments.json (needs make seed)
	go run ./cmd/rankeval -v -min-ndcg 0.95 -min-recall 0.80

BENCH_DOCS ?= 100000
BENCH_SHARDS ?= 1

datagen: ## Load BENCH_DOCS synthetic repositories (BENCH_SHARDS primaries) into repositories_bench
	go run ./cmd/datagen -n $(BENCH_DOCS) -shards $(BENCH_SHARDS)

bench: ## Benchmark every workload against repositories_bench (P50/P95/P99, QPS)
	go run ./cmd/bench -c 8 -d 20s

test: ## Run Go tests
	go test -race ./...

test-integration: ## Run integration tests against the seeded Elasticsearch and PostgreSQL (needs make up && make seed)
	EMBEDDINGS_URL=http://localhost:8000 go test -count=1 -tags integration ./internal/search/ ./internal/store/

lint: ## go vet + gofmt + web lint/typecheck
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	cd web && npm run lint && npx tsc --noEmit

build: ## Build Go binaries into ./bin
	go build -o bin/ ./cmd/...

dev-api: ## Run the API against the compose dependencies
	docker compose up -d postgres redis elasticsearch
	go run ./cmd/api

dev-web: ## Run the Next.js dev server
	cd web && npm run dev
