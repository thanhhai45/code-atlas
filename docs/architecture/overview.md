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

### Ranking

```text
score = BM25(text clauses) + 0.5 × log10(2 + stars) + 1.0 × gauss(pushed_at, offset 30d, scale 180d)
```

Only applied for relevance sort with a text query. Popularity and recency add at most ~3.6 points and act as
tie-breakers between similarly relevant repositories; they no longer multiply the text score. Weights are
`search.DefaultSignals`, chosen with `rankeval -grid` (docs/experiments/03-business-signal-tuning.md).
Sum-mode weights depend on the BM25 score scale, which grows with corpus size, so re-tune after large data changes.

### Pagination

`from + size` is limited by `index.max_result_window` (10,000): Elasticsearch has to collect and sort
`from + size` hits on every shard, so deep offsets get slower and are eventually rejected. Beyond that,
the API uses `search_after`:

- Every sort ends with the unique `id` as a tiebreaker (`_score, stars, id` for relevance), so the order is total
  and a page boundary is unambiguous.
- Every full page returns `next_cursor`: base64url JSON holding the effective sort name and the last hit's sort
  values, kept as exact numbers. The API decodes it into `search_after` and rejects a cursor from another sort order.
- `page` keeps working inside the window, and cursors work from any page, so the UI shows page numbers first
  and switches to cursors at the window edge. A cursor page has no page number and no "previous" (search_after only
  moves forward); the UI offers "first page" instead.
- Cursor pages are not a snapshot. A repository indexed or re-ranked between requests can appear twice or be
  skipped, and recency scores drift slowly over time. A point-in-time (PIT) reader would fix this at the cost of
  server-side state per paging session; it is not needed for browsing.

`internal/search/integration_test.go` (`make test-integration`, and the CI relevance job) checks that paging
through every sort order with cursors visits exactly the documents, in exactly the order, of one large page.

## Caching

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
