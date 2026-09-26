.PHONY: help up down logs seed crawl reindex rankeval rankeval-semantic datagen bench cluster-up cluster-down failover test test-integration lint build dev-api dev-web

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
BENCH_REPLICAS ?= 0

datagen: ## Load BENCH_DOCS synthetic repositories (BENCH_SHARDS primaries) into repositories_bench
	go run ./cmd/datagen -n $(BENCH_DOCS) -shards $(BENCH_SHARDS) -replicas $(BENCH_REPLICAS)

bench: ## Benchmark every workload against repositories_bench (P50/P95/P99, QPS)
	go run ./cmd/bench -c 8 -d 20s

# CLUSTER_FILES=-f infrastructure/cluster/docker-compose.yml alone on hosts that meet the bootstrap checks.
CLUSTER_FILES ?= -f infrastructure/cluster/docker-compose.yml -f infrastructure/cluster/docker-compose.loopback.yml
CLUSTER_URLS ?= http://localhost:9200,http://localhost:9201,http://localhost:9202

cluster-up: ## Start the 3-node Elasticsearch cluster (stop the app stack's elasticsearch first)
	docker compose $(CLUSTER_FILES) up -d

cluster-down: ## Stop the 3-node cluster and delete its data
	docker compose $(CLUSTER_FILES) down -v

failover: ## Kill es03 under load and record client errors and cluster health (needs make cluster-up && make datagen BENCH_REPLICAS=1)
	scripts/cluster-failover.sh -a repositories_bench -n es03 -k 20 -r 25 -d 110 -o failover-results

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
