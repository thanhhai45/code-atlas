# Experiment 01: Elasticsearch fundamentals (lab guide)

Phase 2 of the roadmap. This file is the lab script; fill in the **Result** and **Explanation**
sections with what you observe on your own machine.

## Hypothesis

A search on `description` is served from an inverted index and does not scan every document, so it
stays fast as the index grows, while PostgreSQL `ILIKE '%term%'` degrades linearly without a trigram index.

## Setup

```bash
make up && make seed            # or: make crawl for a larger dataset
```

## Test

### Index, mapping, settings

```bash
curl -s 'localhost:9200/_cat/aliases?v'
curl -s 'localhost:9200/repositories/_mapping?pretty'
curl -s 'localhost:9200/repositories/_settings?pretty'
curl -s 'localhost:9200/_cluster/health?pretty'
```

### What does the analyzer produce?

```bash
curl -s -XPOST 'localhost:9200/repositories/_analyze?pretty' -H 'content-type: application/json' \
  -d '{"field": "name", "text": "go-elasticsearch FastAPI llama_index"}'
curl -s -XPOST 'localhost:9200/repositories/_analyze?pretty' -H 'content-type: application/json' \
  -d '{"analyzer": "repo_text_search", "text": "k8s LLM tools"}'
```

### Segments, refresh, flush, merge

```bash
curl -s 'localhost:9200/_cat/segments/repositories?v'
# Index one document and search immediately with refresh disabled to see near-real-time behavior:
curl -s -XPUT 'localhost:9200/repositories/_settings' -H 'content-type: application/json' -d '{"refresh_interval": "-1"}'
# … index a doc, search for it (not visible), then:
curl -s -XPOST 'localhost:9200/repositories/_refresh'
curl -s -XPOST 'localhost:9200/repositories/_flush'
curl -s -XPOST 'localhost:9200/repositories/_forcemerge?max_num_segments=1'
curl -s 'localhost:9200/_cat/segments/repositories?v'
curl -s -XPUT 'localhost:9200/repositories/_settings' -H 'content-type: application/json' -d '{"refresh_interval": "1s"}'
```

### Elasticsearch vs PostgreSQL LIKE

```bash
docker compose exec postgres psql -U atlas -c "EXPLAIN ANALYZE SELECT full_name FROM repositories WHERE description ILIKE '%database%';"
curl -s -XPOST 'localhost:9200/repositories/_search?pretty' -H 'content-type: application/json' \
  -d '{"profile": true, "query": {"match": {"description": "database"}}}'
```

## Metrics

- Query latency (`took`, `EXPLAIN ANALYZE` execution time) at 50 / 10K / 100K repositories
- Segment count before / after refresh and force-merge

## Result

_TODO_

## Explanation

Questions to answer: why is Elasticsearch fast at search? What is an inverted index? What is a segment?
What do refresh, flush and merge do? When is PostgreSQL `LIKE` good enough?

## Production implication

_TODO_
