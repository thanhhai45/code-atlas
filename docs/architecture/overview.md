# Architecture overview

## Data flow

```text
            ┌──────────────┐   search/suggest/similar   ┌─────────────────┐
 Browser ──►│  Next.js UI  │───────────────────────────►│  Go + Gin API   │
            └──────────────┘  (server components +      └──┬─────┬─────┬──┘
                               /api/suggest proxy)         │     │     │
                                                  detail/list   cache  search
                                                           ▼     ▼     ▼
                                                   PostgreSQL  Redis  Elasticsearch
                                                           ▲                 ▲
                                                 upsert    │                 │ bulk index
                                                           └──── crawler ────┘
                                                                    │
                                                               GitHub API
```

## Decisions

### PostgreSQL is the source of truth; Elasticsearch is derived

The crawler writes to PostgreSQL first, then bulk-indexes into Elasticsearch. If indexing fails, the run
is marked `partial` in `crawl_runs` and the data is recovered with `crawler -reindex`. This means the
search index can always be dropped and rebuilt, which is what makes mapping changes safe.

### Alias + versioned indices from day one

All reads and writes use the alias `repositories`. Concrete indices are named
`repositories_<utc timestamp>`. A reindex:

1. creates a new index with the current `internal/search/index.json`,
2. streams every row from PostgreSQL (keyset pagination) into it with `_bulk`,
3. refreshes it, then atomically moves the alias (`POST /_aliases` with remove + add),
4. deletes the previous index (unless `-keep-old`).

If any document fails, the new index is deleted and the alias is left untouched.

Caveat: writes that happen *during* a reindex go to the old index through the alias. For now, do not run
a crawl and a reindex at the same time; dual-writing is a Phase 13 topic.

### Idempotency

The GitHub numeric id is the PostgreSQL primary key and the Elasticsearch `_id`. Upserts use
`ON CONFLICT DO UPDATE` and bulk `index` actions overwrite, so re-running any crawl is safe.

### Mapping

- `dynamic: strict` — an unexpected field fails loudly instead of silently creating a mapping.
- `name` / `full_name` use a `word_delimiter_graph` analyzer so `FastAPI`, `go-elasticsearch` and
  `llama_index` are searchable by their parts, with `preserve_original` so the whole name still matches.
- `description` / `readme` use `standard + lowercase + asciifolding` at index time and add a
  `synonym_graph` filter at search time only (synonyms can change without reindexing).
- `all_text` is a `copy_to` catch-all (name, description, topics, language, README) used to decide
  *whether* a document matches; per-field `best_fields` + phrase clauses decide *the order*.
- `language`, `license`, `topics` are `keyword` for exact filters and aggregations.
- `full_name.suggest` is `search_as_you_type` for autocomplete.
- README is truncated to 20 KB before storage/indexing.

### Faceting

Facet selections are applied in `post_filter`, not in `query`. Each facet aggregation is wrapped in a
`filter` aggregation containing every *other* selected facet. This keeps counts meaningful for
multi-select ("Go 12, Rust 8" stays visible after selecting Go).

### Ranking (baseline)

```text
score = BM25(text clauses) × ( log(2 + stars) + 0.5 × gauss(pushed_at, offset 30d, scale 180d) )
```

Only applied for relevance sort with a text query. This is a starting point; it must be tuned against a
judgment list with `_rank_eval` (see ROADMAP-REVIEW §3.3).

### Caching

`/search` responses are cached in Redis for `SEARCH_CACHE_TTL_SECONDS` (default 60) under a hash of the
normalized parameters. Redis failures degrade to uncached responses, and `/health` reports Redis as
`degraded`, not down.

## Configuration

| Variable | Default |
|---|---|
| `HTTP_ADDR` | `:8080` |
| `DATABASE_URL` | `postgres://atlas:atlas@localhost:5432/atlas?sslmode=disable` |
| `REDIS_URL` | `redis://localhost:6379/0` |
| `ELASTICSEARCH_URL` | `http://localhost:9200` |
| `SEARCH_ALIAS` | `repositories` |
| `SEARCH_CACHE_TTL_SECONDS` | `60` (0 disables) |
| `GITHUB_TOKEN` | empty (unauthenticated) |
| `GITHUB_API_URL` | `https://api.github.com` |
| `API_URL` (web) | `http://localhost:8080` |
