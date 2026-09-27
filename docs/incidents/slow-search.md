# Search slow for everyone

**Symptoms**: `/search` P95 up (`atlas_http_request_duration_seconds`), node CPU saturated, no errors.

**Check**

```bash
make diagnose                                             # cpu / load per node
curl -s 'localhost:9200/_nodes/hot_threads?threads=3'     # what the search threads are doing
curl -s 'localhost:9200/_tasks?actions=*search*&detailed' # running searches with their source
curl -s localhost:8080/metrics | grep -E 'atlas_search_(cache|coalesced)'   # is the cache working?
```

Turn on the slow log if it is off (it costs nothing until triggered):

```bash
curl -XPUT localhost:9200/repositories/_settings -H 'Content-Type: application/json' \
  -d '{"index.search.slowlog.threshold.query.info":"100ms","index.search.slowlog.threshold.query.warn":"500ms"}'
```

Then **group entries by query shape and rank by count × time**. The slowest single entry is often a cheap query on
a cold shard (experiment 08: an exact term lookup at 901 ms right after a restart).

**Common causes**

- Many identical expensive requests (the empty query computes facets over the whole index): check the cache hit
  rate. Redis down means every request recomputes (`/health` shows `redis: degraded`).
- Cache expiry stampede: `atlas_search_coalesced_total` rising at each TTL means coalescing is absorbing it.
- A pathological query shape (leading wildcard, huge `from`): find it in the slow log or `_tasks`, cancel it with
  `POST /_tasks/<id>/_cancel`.
- Not enough CPU: see experiments 05–07 (more nodes or fewer, cheaper scoring clauses; more shards only help with
  idle cores).

A search `timeout` does not fix this: it is not enforced during aggregation (experiment 08).
