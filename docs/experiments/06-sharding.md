# Experiment 06: Shard count (1, 3, 5 primaries at 1M documents)

Roadmap Phase 11. Experiment 05 left three workloads over target at 1M documents: `full_text` (P95 347 ms),
`full_search` (P95 194 ms) and whole-index aggregations (P50 290 ms). A shard is searched by one thread, so
splitting the index into more shards should let one query use more cores. This experiment measures what that buys
on one node, and what it costs.

## Hypothesis

1. **Latency**: when the node has idle cores (low concurrency), more shards lower the latency of expensive
   queries roughly in proportion to the shard count, up to the core count (4).
2. **Throughput**: when the node is saturated (c = 8 on 4 vCPUs), more shards do not add throughput and cost
   extra per-shard work (term lookups, top-k merges, per-shard aggregation buckets), so throughput drops.
3. **Indexing**: more shards index faster (more parallel Lucene writers).

## Setup

- **Data**: the same 1M synthetic repositories as experiment 05 (`datagen -seed 1 -vectors=false`), loaded once
  per shard count into its own alias:
  `datagen -n 1000000 -vectors=false -shards N -alias repositories_bench_1m_sN`.
  The new `-shards` flag overrides `number_of_shards` in the embedded index definition, and `-force-merge` merges
  every shard to one segment after the load.
- **Workloads**: the 11 lexical workloads of `cmd/bench`, at c = 1 (latency: idle cores available) and c = 8
  (throughput: saturated), 10 s each after 2 s warm-up.
- **Two states**: *as loaded* (whatever segments the bulk load left: 21, 20 and 49 in total) and *force-merged*
  (exactly one segment per shard). The second is the controlled comparison, because segment count is itself a
  source of per-query parallelism (below).
- **Machine**: as in experiment 05: one 4-vCPU container running Elasticsearch 8.15.3 (single node, 0 replicas,
  2 GB heap) and the benchmark client. Absolute numbers move ±20% between runs; compare within a table.
- Raw results: `docs/benchmarks/{datagen,bench}-1m-s{1,3,5}*.json`.

## Indexing

| shards | docs/s | load time | size after load | size force-merged | force-merge time |
|---|---|---|---|---|---|
| 1 | 14,729 | 67.9 s | 1,133 MB (21 segments) | 799 MB | 24 s |
| 3 | 18,730 | 53.4 s | 1,224 MB (47 segments) | 812 MB | 23 s |
| 5 | 16,579 | 60.3 s | 983 MB (67 segments) | 822 MB | 25 s |

Three shards index 27% faster than one. Five shards are slower than three: the 4 bulk workers, the data generator
and 5 writers compete for 4 vCPUs. Sizes after the load only show how far background merging had got. Merged,
the index costs the same at any shard count (+3% for 5 shards: per-shard terms dictionaries).

## Result: as loaded

| workload | c = 1 p50 / p95 ms: 1 shard | 3 shards | 5 shards | c = 8 QPS: 1 shard | 3 shards | 5 shards |
|---|---|---|---|---|---|---|
| exact | 2.8 / 4.4 | 1.8 / 2.6 | 1.9 / 2.5 | 2,045 | 1,797 | 1,334 |
| fuzzy | 3.9 / 5.6 | 3.4 / 5.4 | 4.6 / 7.1 | 1,003 | 683 | 456 |
| bool_filter | 11.2 / 20.8 | 9.9 / 21.1 | 11.4 / 21.5 | 286 | 195 | 130 |
| full_text | 26.2 / 123.1 | 23.4 / 101.9 | 25.2 / 95.9 | 46 | 46 | 38 |
| function_score | 30.4 / 123.8 | 29.3 / 109.5 | 28.0 / 107.2 | 42 | 40 | 37 |
| **full_search** | **41.9 / 108.1** | **21.8 / 46.5** | 24.3 / 46.3 | 75 | 76 | 62 |
| aggregations | 137.4 / 166.7 | 58.4 / 85.3 | 48.2 / 59.0 | 26 | 27 | 28 |
| wildcard | 35.9 / 51.0 | 32.4 / 43.3 | 30.7 / 41.1 | 47 | 46 | 48 |
| deep_from | 43.0 / 63.7 | 31.8 / 44.3 | 36.8 / 45.4 | 97 | 51 | 40 |
| search_after | 20.9 / 33.2 | 11.0 / 16.7 | 10.9 / 15.5 | 166 | 166 | 182 |

At c = 8 the `/search` request (`full_search`) has P95 224 / 177 / 218 ms for 1 / 3 / 5 shards.

## Finding: one shard is already parallel, for some queries

`full_text` barely moved from 1 to 3 shards (P50 26 → 23 ms) while `aggregations` got 2.4× faster. The cause is
concurrent segment search: since 8.12, Elasticsearch splits a shard's segments into slices and runs the query phase
on several threads (`search.query_phase_parallel_collection_enabled`, on by default). It does not do this for every
request. Test on the 1-shard index, same run, c = 1 (`bench-1m-s1-{noparallel,parallel}.json`):

| workload (1 shard, c = 1) | parallel collection off: p50 / p95 | on (default): p50 / p95 |
|---|---|---|
| full_text | 34.4 / 275.7 | 21.6 / 106.8 |
| function_score | 56.2 / 257.2 | 27.7 / 113.1 |
| full_search (with aggregations) | 36.4 / 103.8 | 35.1 / 96.7 |
| aggregations | 146.5 / 182.1 | 147.3 / 188.1 |
| search_after (field sort) | 21.8 / 33.2 | 21.4 / 34.7 |
| deep_from | 41.4 / 56.3 | 41.0 / 54.0 |

Plain scored queries already use several cores on one shard, which halves their P50 and P95. Requests with these
aggregations, or sorted by a field, run on one thread per shard. For them the shard count is the only source of
parallelism. That explains exactly which workloads gained from 3 shards: aggregations, `full_search` (it has five
facet aggregations), `search_after` and `deep_from`.

## Result: force-merged (one segment per shard)

| workload | c = 1 p50 / p95 ms: 1 shard | 3 shards | 5 shards | c = 8 QPS (p95 ms): 1 shard | 3 shards | 5 shards |
|---|---|---|---|---|---|---|
| exact | 1.6 / 2.1 | 1.5 / 2.0 | 1.5 / 2.1 | 3,111 (4.3) | 2,563 (5.0) | 2,207 (5.8) |
| fuzzy | 3.0 / 4.4 | 3.0 / 4.5 | 3.8 / 6.3 | 1,349 (10.5) | 784 (16.6) | 585 (21.7) |
| bool_filter | 8.1 / 21.9 | 6.4 / 13.8 | 7.8 / 15.3 | 411 (42.7) | 250 (53.4) | 180 (74.8) |
| full_text | 24.7 / 223.4 | 17.8 / 101.8 | 18.1 / 85.0 | 54 (443) | 50 (333) | 44 (360) |
| function_score | 46.5 / 251.1 | 24.1 / 101.9 | 25.8 / 96.7 | 45 (531) | 43 (363) | 40 (362) |
| **full_search** | **29.6 / 83.1** | **15.8 / 39.6** | 17.2 / 38.4 | **114 (169)** | 90 (**151**) | 77 (174) |
| aggregations | 142.5 / 161.6 | 52.1 / 76.4 | 46.5 / 59.5 | 28 (383) | 28 (418) | 28 (472) |
| wildcard | 80.0 / 102.5 | 31.6 / 41.5 | 29.2 / 38.4 | 52 (220) | 46 (258) | 48 (262) |
| deep_from | 39.4 / 45.8 | 27.9 / 37.1 | 36.8 / 46.4 | 99 (114) | 54 (220) | 39 (328) |
| search_after | 20.3 / 32.8 | 9.8 / 16.4 | 9.8 / 14.1 | 191 (65) | 175 (73) | 178 (74) |

Force-merging one shard to one segment removes its slices. So `full_text` P95 at c = 1 doubles (102 → 223 ms)
while cheap queries get faster. A 1-shard index therefore has to choose between segments for latency and a merged
index for throughput. With 3 shards, force-merging is a pure gain: `full_search` 15.8 / 39.6 ms at c = 1, and the
best c = 8 tail (P95 151 ms).

## Explanation

- **Hypothesis 1 holds for single-threaded requests, not for all.** Aggregations, `search_after` and `/search` get
  1.9–2.4× lower latency with 3 shards (`deep_from` 1.35×): `full_search` P50 42 → 22 ms, aggregations P50 137 → 58 ms. Plain scored
  queries were already parallel through segment slices, so they gain little. The speed-up stops at 3: 5 shards give
  nothing more on 4 vCPUs and are slower for cheap queries.
- **Hypothesis 2 holds.** At saturation, each extra shard is extra work per request. Cheap queries lose the most,
  because per-shard overhead dominates them: force-merged `bool_filter` 411 → 250 → 180 QPS, `fuzzy`
  1,349 → 784 → 585, `deep_from` 99 → 54 → 39 (every shard has to collect `from + size` hits). Expensive queries,
  whose work is proportional to matches rather than shards, stay flat (`aggregations` 28 QPS at every count).
- **Hypothesis 3 holds up to the core count**: 3 shards are fastest; 5 shards compete for CPU with the bulk clients.
- **Sharding is not a fix for the 1M targets.** Even at 3 shards, `full_text` at c = 8 has P95 333 ms: once all
  cores are busy, only less work per query (experiment 05's list) or more cores help. Sharding helps only when
  there are idle cores to spread over.

## Production implication

- **Use 3 primaries for the 1M tier.** It halves `/search` latency at normal load and has the best tail at
  saturation. It costs 12–45% throughput on cheap queries, which have a lot of headroom (hundreds of QPS at
  single-digit milliseconds). The choice follows CPU parallelism, not size: shards are ~270 MB, far below the
  usual 10–50 GB per shard guidance. Keep `index.json` at 1 shard for now, because the live index holds the
  crawl of thousands of repositories, not 1M. Set 3 shards when a reindex crosses about 500K documents, and
  revisit on the multi-node cluster, where shards also spread across machines.
- **Do not go past the core count.** 5 shards were never better than 3 here. The roadmap's 10-shard case was not
  run: at ~80 MB per shard it is plainly oversharded, and the 5-shard results already show the trend.
- **Force-merge after a full reindex**, before the alias swap: 24 s at 1M, index 30% smaller, and faster queries
  with 3 shards. Do not force-merge an index that keeps taking heavy writes: a merged segment that later collects
  deletes is not merged again until it is mostly deletes. The crawler's small incremental upserts are fine.
- **Replicas were not measured.** Elasticsearch never allocates a replica on the node that holds its primary, so
  on one node `number_of_replicas: 1` only turns the cluster yellow. Replicas add throughput and survive node loss
  only across nodes. Measure them in Phase 12 on a 3-node Docker Compose cluster, together with recovery time.
