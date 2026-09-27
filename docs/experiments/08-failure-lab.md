# Experiment 08: Failure lab (expensive requests, slow log, heap, disk, unassigned shards)

Roadmap Phase 13. Experiment 07 covered node failure and a red cluster. This one breaks the cluster in the other
ways the roadmap lists and asks the same question each time: *what do users and operators see, and what should
the system do about it?*

Runbooks derived from it: [`docs/incidents/`](../incidents/README.md).

## Setup

- The three-node cluster of experiment 07 (`infrastructure/cluster`, each node 1 CPU and a 1 GB heap, loopback
  override), the 1M-document index with 3 primaries and no replica, and the API (`cmd/api`) with Redis.
- Load from `cmd/bench` (new: `-timeout` adds a per-request Elasticsearch `timeout` to its searches) and curl.
- Raw results: `docs/benchmarks/failure-lab/`. Same caveats as before: one host, so absolute numbers only compare
  within a table.

## 1. Expensive requests next to normal search (high CPU)

The API's most expensive legitimate request is the empty query (the home page): five facet aggregations over all
1M documents (`aggregations` workload, ~110 ms per shard). Victim: the `/search` request (`full_search`) at c = 4.
Noise: home-page requests at c = 4. Warm cluster, 30 s each.

| run | victim QPS | victim p50 / p95 ms | noise |
|---|---|---|---|
| victim alone | 42.3 – 46.9 | 80 – 89 / 166 – 181 | — |
| + home-page facets | **11.4** | 351 / 527 | 14.0 QPS, p50 268 ms |
| + home-page facets with `timeout: 100ms` | 13.2 | 297 / 464 | 17.0 QPS; **82% returned partial facets**; p50 still 208 ms |
| + home-page requests through the API with Redis | 32.3 | 114 / 238 | 7,061 requests in 20 s, all but the first from the cache (1.4 ms) |

- Four clients browsing the home page cut everyone else's search throughput by 75% and tripled its P95.
- **A search `timeout` is a weak defence.** It is checked while shards collect documents, not during aggregation
  and reduction, so a 100 ms timeout still let requests run ~210 ms, and it bought the victim only 16%, at the
  price of wrong facet counts for 82% of the noisy requests. It bounds pathological queries; it does not protect
  neighbours.
- **The cache is the defence**: identical requests are not recomputed. The remaining gap to "victim alone" is the
  curl clients and the API competing with the benchmark for the same host CPU.

### Cache stampede

A cache only helps between expiries. When the home-page entry expires, every concurrent request misses at once and
runs the same aggregation. Measured with a 5 s TTL and 16 clients for 30 s (ideal: one search per expiry, ~6):

| | home-page requests | searches sent to Elasticsearch |
|---|---|---|
| before | 16,957 | **80** |
| with request coalescing | 19,085 | **6** (96 requests shared an in-flight search) |

**Fixed**: the API now coalesces identical searches that miss the cache at the same time into one Elasticsearch
request (`singleflight` on the cache key). The shared search runs detached from any single client, so one client
disconnecting does not fail the others, and is bounded to 10 s. `atlas_search_coalesced_total` counts shared
requests.

## 2. Slow log

Thresholds on the 1M index (`index.search.slowlog.threshold.query.info: 100ms`, `.warn: 300ms`), then 5 s of mixed
traffic right after a cluster restart:

| query shape | INFO (≥ 100 ms) | WARN (≥ 300 ms) | slowest |
|---|---|---|---|
| home-page facets | 76 | 0 | 187 ms |
| leading wildcard | 21 | 3 | 700 ms |
| `/search` | 7 | 0 | 170 ms |
| **exact term lookup** (normally ~3 ms) | 3 | **5** | **901 ms** |

- The slowest entries were the *cheapest* query: the first term lookups on shards that were cold after the restart.
  Sorting the slow log by `took` would have blamed the wrong query. **Group entries by query shape and rank by
  count × time**: here the facets dominate (76 entries), which matches section 1.
- Entries are per shard (`[index][shard]` in the message) and include the full request source, which is what makes
  grouping by shape possible. Enable INFO/WARN thresholds on production indices; they cost nothing until triggered.

## 3. Heap: one huge aggregation

A terms aggregation on `full_name.keyword` (1M distinct values), while `/search` ran at c = 2:

| request | result |
|---|---|
| `size: 50000` | OK in 1.5 s; data-node heap 8% → 62% |
| `size: 500000`, `size: 1000000` | **HTTP 400 `too_many_buckets_exception`** after 2–3 s (`search.max_buckets`: 65,536) |
| `search.max_buckets` raised to 2M, one 1M-bucket request | OK in 7 s; coordinating node heap 85% |
| same, **four at once** | **es01 died: `OutOfMemoryError`**, a 1.4 GB heap dump written to its data directory (82 s), cluster red |

- `/search` kept working through the rejected requests (no errors, P95 310 ms against ~95 ms for a few seconds).
- `search.max_buckets` is the protection that works. The circuit breakers never tripped: they estimate memory as
  requests go, and four large reductions on the coordinating node outran them. **Do not raise `max_buckets`**, and
  never let a client choose aggregation sizes (the API's facet sizes are fixed).
- The heap dump lands on the data disk (`-XX:HeapDumpPath=data`). A node that keeps running out of memory can fill
  the disk and trigger section 4. Point `HeapDumpPath` elsewhere or clean up dumps after an incident.
- The cluster went red only because the test index had no replica: experiment 07 again.

## 4. Disk pressure (flood stage)

The watermarks were set as free-space thresholds above the free space the nodes have, so every node exceeded the
flood stage.

- After **7 s** Elasticsearch set `index.blocks.read_only_allow_delete` on every index with a shard on those nodes.
  Searches kept working. Every write failed with HTTP 429 `cluster_block_exception`.
- The crawler kept going: PostgreSQL got the new data, Elasticsearch did not, and the process **exited with status 0**.
  Only `crawl_runs.status = partial` and warning lines recorded that search was now stale. **Fixed**: the crawler now
  fails when any repository could not be indexed and says what to do (repair Elasticsearch, then `crawler -reindex`).
- After the thresholds were removed (the equivalent of freeing disk), Elasticsearch released the block by itself
  after **16 s** (since 7.4 it does so once usage falls below the high watermark).
- Note: Elasticsearch reports this host's disk at 91% used, above the default 90% high watermark. That is why the
  development compose files disable disk thresholds; production must keep them.

## 5. Unassigned shards

Two broken indices: one whose primary requires a node attribute no node has
(`index.routing.allocation.require.box_type: hot`, red), one with 3 replicas on 3 nodes (yellow), plus a manual
write block. `make diagnose` (`scripts/cluster-diagnose.sh`, read-only) reported all three and, from
`_cluster/allocation/explain`, the exact reasons:

```text
-- why is diag_filter shard 0 (p) unassigned?
  [filter] node does not match index setting [index.routing.allocation.require] filters [box_type:"hot"]
-- why is diag_replicas shard 0 (r) unassigned?
  [same_shard] a copy of this shard is already allocated to this node ...
```

Unassigned shards always have a decider saying no; the allocation explain API names it. The script puts that first,
next to node heap and disk, tripped breakers and write-blocked indices.

## Production implication

- **Cache and coalesce expensive identical requests.** A handful of home-page visitors can take 75% of search
  capacity; the cache removes them, and coalescing removes the stampede at every expiry. Precomputing the empty-query
  facets would remove even that.
- **Do not rely on the search `timeout` to protect other traffic.** It bounds runaway queries (and `/search` flags
  timed-out responses as partial, never caching them), but aggregations overrun it.
- **Keep `search.max_buckets` at its default and fixed facet sizes.** Past it, four requests killed a node.
- **Alert on**: `atlas_search_partial_results_total` (a shard had no copy or timed out), crawler exit status and
  `crawl_runs.status != succeeded`, node heap above ~85%, disk above the high watermark, any tripped breaker.
- **Slow log on, grouped by query shape.** The slowest single entry is often a cold cache, not the costly query.
- **First response to a red or yellow cluster**: `make diagnose`, then the matching runbook in `docs/incidents/`.
