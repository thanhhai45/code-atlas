# Code Atlas

An open-source **discovery & intelligence** search engine: find, filter and compare open-source
repositories — "Go vector databases", "Ruby Elasticsearch libraries under MIT",
"active RAG frameworks with 5K+ stars" — built on Elasticsearch.

It is also a learning project that takes Elasticsearch from application development to relevance
engineering, scaling and production operations. See [`docs/ROADMAP.md`](docs/ROADMAP.md) for the plan and
[`docs/ROADMAP-REVIEW.md`](docs/ROADMAP-REVIEW.md) for the review and current status.

```text
GitHub API ──► crawler (Go) ──► PostgreSQL (source of truth)
                     │
                     └────────► Elasticsearch (alias → versioned index) ◄── API (Go + Gin) ◄── Web (Next.js)
                                                                              │
                                                                            Redis (cache)
```

## Features (MVP so far)

- **Full-text search** with field boosts, phrase boost, fuzzy matching (`elastisearch` → elasticsearch)
  and tech synonyms (`k8s` ↔ kubernetes, `llm` ↔ large language model, …)
- **Ranking**: BM25 + small popularity and recency tie-breakers via `function_score`, weights tuned with `rankeval -grid`
- **Multi-select facets**: language, license, topics, star ranges, activity — counts stay correct
  when filters are selected (`post_filter` + per-facet filter aggregations)
- **Autocomplete** (`search_as_you_type`), **highlighting**, sorting, pagination
- **Semantic and hybrid search** (opt-in `mode=semantic|hybrid`): dense embeddings from the `ai-worker`
  (all-MiniLM-L6-v2 on CPU) with Elasticsearch kNN; lexical stays the default because it measured best
  ([experiment 04](docs/experiments/04-semantic-hybrid-search.md))
- **Similar repositories** by embedding nearest neighbours (`more_like_this` fallback)
- **Crawler**: GitHub pagination, primary/secondary rate limits, retry with backoff + jitter,
  idempotent upserts, bulk indexing, "star cursor" to get past the 1000-results-per-query cap
- **Zero-downtime reindex**: build a new versioned index from PostgreSQL, then swap the alias atomically
- **Relevance evaluation**: a graded judgment list (`testdata/judgments.json`) scored with the
  Elasticsearch Ranking Evaluation API (NDCG, MRR, precision, recall); CI fails on regressions
- **Observability**: Prometheus `/metrics` (HTTP latency, ES `took`, cache hit rate), `/health`

## Quick start

Requirements: Docker with Compose. Elasticsearch needs ~2 GB RAM.

```bash
cp .env.example .env          # optionally set GITHUB_TOKEN
make up                       # postgres, redis, elasticsearch, ai-worker, api (:8080), web (:3000)
make seed                     # load 50 bundled sample repositories (with embeddings)
open http://localhost:3000
```

Crawl real data from GitHub (a token is strongly recommended):

```bash
make crawl CRAWL_MIN_STARS=1000 CRAWL_MAX=10000
docker compose run --rm crawler -q "language:go topic:database" -min-stars 50 -readme
```

> `testdata/seed_repositories.json` is hand-written sample data for offline development. Star counts
> and dates are approximate and ids are synthetic (900000001+); run the crawler for real data.

### Local development without containers for the apps

```bash
make dev-api     # starts postgres/redis/elasticsearch in docker, runs the API with go run
make dev-web     # Next.js dev server on :3000 (API_URL defaults to http://localhost:8080)
go run ./cmd/crawler -seed testdata/seed_repositories.json
```

## API

| Method | Path | Description |
|---|---|---|
| GET | `/health` | Postgres / Elasticsearch / Redis status (503 if a required dependency is down) |
| GET | `/metrics` | Prometheus metrics |
| GET | `/repositories?page=&size=` | List from PostgreSQL, ordered by stars |
| GET | `/repositories/:id` | Repository detail (id = GitHub id) |
| GET | `/repositories/:id/similar` | Similar repositories |
| GET | `/search` | Faceted search (below) |
| GET | `/suggest?q=` | Autocomplete |

`/search` parameters: `q`, `language`, `license`, `topic` (repeatable or comma-separated),
`min_stars`, `max_stars`, `pushed_within` (`30d` \| `90d` \| `1y`), `sort` (`relevance` \| `stars` \| `updated`),
`page`, `size` (≤ 100), `cursor`, `mode` (`lexical` default \| `hybrid` \| `semantic`), `include_archived`,
`include_forks`. The response's `mode` field is the mode that actually ran: semantic modes need a text query,
"best match" ordering and a reachable ai-worker, and fall back to lexical otherwise.

Pagination: `page` works up to the 10,000-result window. Every full page also returns `next_cursor`; pass it
back as `cursor` (with the same query, filters and sort) to fetch the next page with `search_after`, at any depth.
A cursor for a different sort order is rejected with 400.

```bash
curl 'localhost:8080/search?q=vector+database&language=Go&min_stars=5000'
```

## Crawler

```text
crawler [flags]
  -q string         extra GitHub search qualifiers, e.g. "language:go topic:database"
  -min-stars int    minimum stars (default 100)
  -max int          maximum repositories to fetch, 0 = no limit (default 1000)
  -readme           also fetch READMEs (one API call per repository)
  -seed file        load repositories from JSON instead of GitHub
  -reindex          rebuild the index from PostgreSQL into a new index and swap the alias
  -keep-old         with -reindex, keep the previous index
```

**Upgrading an existing index.** Mapping changes (such as the `embedding` field added for semantic search) need
a new index: run `make reindex` (`crawler -reindex`). It builds a new versioned index from PostgreSQL, backfills
missing embeddings when `EMBEDDINGS_URL` is set, and swaps the alias with no downtime.

Every run is recorded in the `crawl_runs` table (fetched / indexed / failed / status).

Repositories are keyed by GitHub id; a full name belongs to whichever id holds it now. When a crawled
repository's name is still held by another row (renamed, transferred or deleted on GitHub, or the bundled
sample data, which uses real names with synthetic ids), that stale row is deleted from PostgreSQL and the index.

**Getting past the 1000-result cap.** The GitHub Search API returns at most 1000 results per query, so the
crawler splits the crawl into many queries:

1. *Star cursor*: sort by stars descending; when a query is capped, the next one is `stars:<min>..<lowest seen>`
   (duplicates at the boundary are dropped by id).
2. *Creation-date slicing*: when at least 1000 repositories share one star count `S` (common below ~100 stars),
   the cursor cannot move. The crawler then fetches `stars:S created:A..B`, bisecting the date range until each
   slice has ≤ 1000 results, and continues with `stars:<min>..S-1`.

Each 100 repositories cost one Search API request (plus about one extra request per bisection step), and the
authenticated limit is 30 requests/minute, so plan for roughly 3,000 repositories per minute at best.

## Relevance evaluation

```bash
make seed
make rankeval           # NDCG@10, MRR@10, precision@5, recall@10 for the default and BM25-only rankings
make rankeval-semantic  # also semantic / hybrid configurations, plus the unrated pool to judge
```

`cmd/rankeval` runs every query in `testdata/judgments.json` through `_rank_eval`, prints overall and
per-query scores, lists unrated documents that appear in the top 10 (judge them and add them to the
file), and exits non-zero below `-min-ndcg` / `-min-recall`. `-grid` also evaluates 30 business-signal
weightings and reports the best one ([experiment 03](docs/experiments/03-business-signal-tuning.md)). Baseline and experiments:
[`docs/experiments/02-ranking-evaluation.md`](docs/experiments/02-ranking-evaluation.md).

## Benchmarks

```bash
make datagen BENCH_DOCS=100000   # synthetic repositories into the repositories_bench alias
make bench                       # 16 workloads, 8 workers, 20 s each: P50/P95/P99, QPS, errors
go run ./cmd/bench -list         # workload descriptions
go run ./cmd/bench -workloads full_search,hybrid -c 1,4,16 -d 30s -json out.json
go run ./cmd/datagen -n 1000000 -vectors=false -shards 3 -force-merge -alias repositories_bench_s3
```

`cmd/datagen` generates realistic data at any scale (power-law stars, Zipf topics and vocabulary, topic-clustered
embeddings), loads it into its own alias (never the live one) and reports indexing throughput. `cmd/bench` runs
the roadmap's query types (exact, full text, bool + filter, function score, aggregations, fuzzy, wildcard,
suggest, kNN, hybrid, deep `from` vs `search_after`, and the complete `/search` request) against Elasticsearch
or, with `-target api`, through the Go API. Results and analysis:
[`docs/experiments/05-benchmark-baseline.md`](docs/experiments/05-benchmark-baseline.md). Shard count (1 / 3 / 5
primaries at 1M documents, `-shards`, `-force-merge`):
[`docs/experiments/06-sharding.md`](docs/experiments/06-sharding.md).

## Repository layout

```text
cmd/api            HTTP API entrypoint
cmd/crawler        GitHub ingestion + reindex entrypoint
cmd/rankeval       Relevance evaluation against a judgment list
cmd/datagen        Synthetic data loader for benchmarks (internal/synth)
cmd/bench          Latency / throughput benchmark (internal/bench)
internal/embed     ai-worker client
internal/api       Gin handlers
internal/search    Elasticsearch client, index definition (index.json), query builder
internal/github    GitHub REST client (pagination, rate limits, retries)
internal/store     PostgreSQL store + embedded migrations
internal/cache     Best-effort Redis cache
internal/metrics   Prometheus instruments
web/               Next.js + TypeScript + Tailwind UI
ai-worker/         Python + FastAPI embedding service (fastembed / ONNX, no GPU)
infrastructure/    Dockerfiles (Terraform/AWS later)
docs/              Roadmap, review, architecture, experiments, benchmarks, incidents
testdata/          Sample seed data and relevance judgments
```

## Development

```bash
make test              # go test -race ./...
make test-integration  # tests that need the seeded Elasticsearch (search_after paging)
make lint    # go vet, gofmt, eslint, tsc
```

## License

GPL-3.0 — see [LICENSE](LICENSE).
