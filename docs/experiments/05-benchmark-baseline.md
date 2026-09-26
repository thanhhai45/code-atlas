# Experiment 05: Benchmark baseline (100K and 1M documents)

Roadmap Phase 10. This experiment builds the benchmark tooling, records a latency / throughput baseline for
every query type the roadmap lists, and uses it to find and fix the slowest part of `/search`.

## Hypothesis

1. At 100K documents every query type meets the roadmap targets (P50 < 50 ms, P95 < 100 ms, P99 < 200 ms).
2. Latency grows with the number of *matching* documents, not the index size: dictionary lookups (exact,
   fuzzy, prefix) stay flat from 100K to 1M, while scoring- and aggregation-heavy queries grow roughly linearly.
3. `search_after` is cheaper than deep `from + size` (the Phase 6 decision).

## Setup

- **Data**: `cmd/datagen` synthetic repositories (`internal/synth`): Pareto stars, Zipf topics and vocabulary,
  weighted languages and licenses, a 3–7 sentence README, and topic-clustered 384-dim embeddings. The real crawl
  cannot produce 1M documents in reasonable time (Search API: ~3,000 repos/minute) and is not reproducible.
  The synthetic data is. The caveat: its vocabulary is much smaller than real READMEs, so posting lists are
  denser, which inflates full-text cost compared to real data of the same size.
- **Tool**: `cmd/bench`, a closed-loop load generator. Each of *c* workers sends its next request when the
  previous one returns, with warm-up, then P50/P95/P99/max, QPS, error rate, Elasticsearch's own `took`, and
  mean hits (a guard against workloads that accidentally match nothing). Every workload reuses the production
  query builders, so `full_search` is byte-for-byte the `/search` request.
- **Machine**: one 4-vCPU, 15 GB container running Elasticsearch 8.15.3 (single node, 1 shard, 0 replicas, 2 GB
  heap) **and** the benchmark client. The client competes with Elasticsearch for CPU, so absolute numbers are
  pessimistic and saturation appears early. Compare runs only within this document.
- **Runs**: 100K documents with embeddings, and 1M documents without (lexical workloads only), c = 8,
  15 s (100K) / 10 s (1M) per workload after warm-up. Concurrency sweep at 100K: c = 1, 4, 16.
  Raw results: `docs/benchmarks/{datagen,bench}-*.json`.

## Indexing throughput

| data set | docs/s | load time | index size | bytes/doc | segments after load |
|---|---|---|---|---|---|
| 100K, with embeddings | 3,853 | 26.0 s | 634.5 MB | 6,345 | 6 |
| 1M, no embeddings | 15,313 | 65.3 s | 1,148.7 MB | 1,149 | 28 |

Loaded with `refresh_interval: -1`, 0 replicas, 4 × 1,000-document `_bulk` workers, then restored to 1 s and
refreshed. Embeddings cost about 4× in throughput and 5.5× in size per document. Most of that is the vector
stored as JSON floats in `_source`, on top of the int8 HNSW graph.

## Finding: highlighting tripled `/search` latency

The first 100K run put the complete `/search` request far above target: **P50 75.7 ms, P95 248 ms, 78 QPS**,
while the same text query without facets or highlighting (`full_text`) had a P95 of 86 ms. Three diagnostic
variants each remove one component (`docs/benchmarks/bench-100k-full-search-breakdown.json`):

| variant (100K, c = 8) | p50 ms | p95 ms | p99 ms | QPS |
|---|---|---|---|---|
| full_search (before fix) | 74.8 | 251.5 | 331.6 | 78 |
| without facet aggregations | 68.8 | 242.0 | 321.7 | 84 |
| without `track_total_hits: true` | 74.0 | 252.3 | 338.8 | 78 |
| **without highlighting** | **26.3** | **48.2** | 61.3 | **284** |

Highlighting was the cost. Two hypotheses:

- **(a) The highlighter re-runs the search query**, including its `fuzziness: AUTO` clause, and has to expand
  fuzzy terms for every hit. Test: a dedicated non-fuzzy `highlight_query`. Result: **P50 30.7 ms, P95 56.4 ms**,
  nearly the no-highlight numbers.
- **(b) Re-analyzing `_source` text is slow**, so index `offsets` would let the unified highlighter read
  postings instead. Test: same data with `index_options: offsets` on `description` and `readme`. Result:
  full_search stayed at P50 92.5 ms / P95 286.5 ms, and the index was slightly larger. **Rejected.**

**Fix shipped**: `/search` highlights with `highlight_query: multi_match(q, [description, readme])`, which covers
exact and synonym matches and skips fuzzy expansion. Trade-off: a typo query such as "elastisearch" still
*finds* Elasticsearch but no longer highlights the corrected word.

## Result: 100K documents, c = 8 (after the fix)

| workload | p50 ms | p95 ms | p99 ms | QPS | ES took ms | mean hits |
|---|---|---|---|---|---|---|
| exact (term on keyword) | 3.8 | 8.2 | 11.6 | 1,833 | 1.7 | 1 |
| full_text (BM25, no signals) | 28.0 | 77.4 | 101.4 | 236 | 30.5 | 5,602 |
| bool_filter (text + 3 filters) | 13.0 | 24.7 | 32.1 | 568 | 11.4 | 57 |
| function_score (text + signals) | 27.6 | 71.8 | 95.5 | 245 | 29.3 | 5,604 |
| aggregations (5 facets, whole index) | 30.4 | 48.7 | 56.4 | 253 | 28.6 | 92,000 |
| **full_search** (the `/search` request) | **27.7** | **55.0** | **71.0** | **265** | 25.7 | 187 |
| fuzzy | 6.0 | 11.2 | 14.7 | 1,237 | 3.9 | 5,368 |
| wildcard (`*frag*` on keyword) | 20.8 | 32.7 | 38.9 | 372 | 18.7 | 2,331 |
| suggest (search_as_you_type) | 4.3 | 8.2 | 11.5 | 1,673 | 2.5 | 4,479 |
| knn (num_candidates 100) | 8.3 | 13.6 | 17.2 | 910 | 5.6 | 100 |
| hybrid (BM25 + signals + kNN) | 27.2 | 63.7 | 77.3 | 255 | 27.8 | 6,760 |
| deep_from (from ≈ 9,000–9,980) | 34.1 | 54.1 | 65.7 | 226 | 31.6 | 92,000 |
| search_after (any depth) | 9.3 | 15.8 | 20.0 | 805 | 6.8 | 92,000 |

`full_search` went from P50 75.7 / P95 248 ms and 78 QPS to **27.7 / 55.0 ms and 265 QPS (3.4×)**. Every
workload meets the roadmap targets at 100K. (Absolute numbers move ±20% between container restarts; e.g.
`aggregations` measured 45 ms P50 in the first run. Compare numbers from the same run.)

### Concurrency sweep (100K)

| workload | c = 1: p50 / p95 / QPS | c = 4 | c = 16 |
|---|---|---|---|
| exact | 1.6 / 2.2 / 573 | 1.8 / 2.8 / 2,020 | 4.7 / 9.0 / 3,096 |
| full_text | 11.0 / 35.6 / 68 | 17.3 / 46.6 / 190 | 52.4 / 102.7 / 269 |
| full_search | 11.5 / 21.4 / 79 | 17.4 / 31.5 / 214 | 47.6 / 84.7 / 302 |
| knn | 3.1 / 4.1 / 300 | 4.8 / 7.2 / 787 | 14.3 / 22.9 / 1,057 |
| hybrid | 12.3 / 28.5 / 68 | 19.3 / 42.1 / 185 | 54.2 / 101.0 / 263 |

Throughput nearly triples from c = 1 to 4, then gains only ~40% from 4 to 16 while latency triples. That is
CPU saturation of the 4 vCPUs shared by Elasticsearch and the client. Past saturation, extra concurrency
turns into queueing, not throughput.

## Result: 1M documents (no embeddings), c = 8

| workload | 100K p50 / p95 | **1M p50 / p95** | ratio (p50) | mean hits at 1M |
|---|---|---|---|---|
| exact | 3.8 / 8.2 | 4.1 / 8.1 | 1.1× | 1 |
| fuzzy | 6.0 / 11.2 | 6.9 / 12.7 | 1.2× | 8,396 |
| suggest | 4.3 / 8.2 | 8.1 / 19.3 | 1.9× | 8,986 |
| bool_filter | 13.0 / 24.7 | 25.6 / 50.6 | 2.0× | 485 |
| full_search | 27.7 / 55.0 | 69.9 / 193.5 | 2.5× | 1,935 |
| deep_from | 34.1 / 54.1 | 80.1 / 116.4 | 2.3× | 921,343 |
| search_after | 9.3 / 15.8 | 44.3 / 70.4 | 4.8× | 921,343 |
| full_text | 28.0 / 77.4 | 147.4 / 347.4 | 5.3× | 8,239 |
| function_score | 27.6 / 71.8 | 156.3 / 344.9 | 5.7× | 8,314 |
| wildcard | 20.8 / 32.7 | 159.3 / 195.8 | 7.7× | 5,877 |
| aggregations | 30.4 / 48.7 | 289.7 / 416.1 | 9.5× | 921,343 |

At 1M, `full_search` breaks down as: without aggregations P50 46.3 / P95 119.5 ms; without highlighting 61.3 /
187.4 ms. With ~2,000 hits per query, facets now matter and highlighting no longer dominates.

## Explanation

- **Hypothesis 1 holds** at 100K after the highlight fix: every workload is within P50 < 50 ms, P95 < 100 ms and
  P99 < 200 ms at c = 8.
- **Hypothesis 2 holds.**
  - Dictionary lookups stay flat: `exact` and `fuzzy` cost 1.1–1.2× at 10× the data.
  - Aggregations over the whole index scale with the documents they visit (9.5×).
  - A leading wildcard scans the terms dictionary (7.7×).
  - Full-text scoring grows about 5×. It is sub-linear only because Zipf queries mostly hit the same popular terms.
  - A profile of 8 `full_text` queries at 1M shows no single hot clause: `best_fields` scoring over five fields
    36%, the recall gate (`all_text` + fuzzy) 31%, the phrase clause 14%, filters 9%. The cost is scoring
    thousands of matches per query.
- **Hypothesis 3 holds**: at 100K, `search_after` is 3.7× cheaper than a `from` near 9,000. At 1M it is still
  1.8× cheaper, although the benchmark's `track_total_hits: true` makes both count all 921K matches.
- **Roadmap targets at 1M are not met** for `full_text` (P95 347 ms), `full_search` (P95 194 ms) and
  whole-index aggregations. On one shared 4-vCPU node that is expected. It sets the agenda for Phase 11–12.

## Production implication

- The highlight fix is the main shipped change: 3.4× throughput for `/search` at 100K, with no index change.
- Next experiments, each measurable with `cmd/bench`:
  - Phase 11: 1 vs 3 shards (parallelism per query) and replicas (throughput).
  - Force-merge to one segment after bulk loads (28 segments at 1M).
  - Score fewer fields: drop `readme` from `best_fields`, or score on `all_text` only; verify relevance with `rankeval`.
  - Aggregate facets with `execution_hint`, or on a filtered sample, or cache them per query.
  - `track_total_hits: 10000` once hit counts exceed it (no effect at the current hit counts).
- Run benchmarks on a machine where the client does not share CPU with Elasticsearch before comparing absolute
  numbers with the roadmap targets.
- CI runs a smoke test (`datagen -n 3000`, every workload for 2 s, zero errors allowed), so no workload rots, but
  CI numbers are not a performance gate: shared runners are too noisy.
