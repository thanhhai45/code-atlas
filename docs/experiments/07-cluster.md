# Experiment 07: Three-node cluster, replicas and node failure

Roadmap Phase 12, plus the node-failure part of Phase 13. Experiment 06 measured shard counts on one node and
left replicas open, because Elasticsearch never puts a replica on the node that holds its primary. This experiment
moves to a three-node cluster and asks three questions:

1. What do more nodes buy, and how should shards and replicas be laid out across them?
2. What does a replica cost at indexing time?
3. What do clients see when a node dies?

## Hypothesis

1. **Scale-out**: three nodes serve about 3× the throughput of one node of the same size, for every query type.
2. **Layout**: with 3 primaries on 3 nodes, every query already uses every node, so replicas add no throughput;
   they only add fault tolerance. With 1 primary and 2 replicas, each query runs on one node, which gives the
   best throughput for cheap queries but no latency gain.
3. **Indexing**: indexing with replicas enabled costs ~2× (every document is indexed twice); adding replicas
   after the load is cheaper, because a replica is then a file copy of finished segments.
4. **Failure**: with one replica, losing a node causes no failed requests (replicas are promoted), and a node that
   comes back quickly recovers without copying its data again. With no replica, the cluster goes red and searches
   fail.

## Setup

- **Cluster**: `infrastructure/cluster/docker-compose.yml`: three Elasticsearch 8.15.3 nodes (`es01`–`es03`), all
  master-eligible data nodes, **each capped at 1 CPU** and a 1 GB heap (`cpus: 1`, `mem_limit: 3g`). The cap makes
  three containers on one 4-vCPU host behave like three small machines instead of sharing all cores.
  - A multi-node cluster enforces the bootstrap checks (65,535 open files, `vm.max_map_count` 262,144). This host
    caps open files at 20,000, so the runs used `docker-compose.loopback.yml`: host networking, each node on
    127.0.0.1 with its own ports. Elasticsearch skips the checks on loopback. Discovery, replication and recovery
    are the same as across machines; the network between nodes is faster than a real one.
- **Baseline**: one node with the same cap (1 CPU, 1 GB heap), outside the cluster.
- **Data**: the 1M synthetic repositories of experiments 05–06 (`datagen -vectors=false -force-merge`), one index
  with 1 primary and one with 3 primaries. Replicas were changed live with `number_of_replicas` (a dynamic setting).
- **Client**: `cmd/bench`, now able to round-robin over several node URLs and retry a request once on the next
  node when a node is unreachable, as Elasticsearch clients do. The client gets the host's fourth vCPU. At c = 8 the
  cheapest workloads (`exact`, ~1,500 QPS) are probably limited by the client, not the cluster.
- **Failure tests**: `scripts/cluster-failover.sh` runs `full_search` at c = 4 with a 1 s timeline, polls
  `_cluster/health` every second from any live node, `docker kill`s a node (SIGKILL, no clean shutdown) and
  starts it again later.
- Raw results: `docs/benchmarks/{datagen,bench}-{single,cluster}-*.json` and `docs/benchmarks/failover/`.

## Result: scale-out and layout

`p1r2` = 1 primary + 2 replicas. On the cluster, the index with 1 primary and 0 replicas lives on one node, so
`cluster p1r0` should match the single node: it does, which checks the setup.

**Latency with idle capacity (c = 1), p50 / p95 ms**

| workload | 1 node, 1 shard | 1 node, 3 shards | cluster p1r0 | cluster p1r2 | **cluster p3r0** | cluster p3r1 | cluster p3r2 |
|---|---|---|---|---|---|---|---|
| exact | 3.3 / 5.4 | 1.9 / 2.9 | 4.6 / 8.2 | 2.5 / 4.5 | 2.8 / 4.2 | 2.5 / 3.4 | 2.6 / 3.7 |
| bool_filter | 11.3 / 69.5 | 11.1 / 57.0 | 14.5 / 63.2 | 11.3 / 40.1 | 8.6 / 19.5 | 8.1 / 16.6 | 8.6 / 17.1 |
| full_text | 32.5 / 294 | 35.3 / 282 | 30.3 / 263 | 28.0 / 282 | 18.6 / 101 | 21.2 / 102 | 20.7 / 103 |
| **full_search** | 64.2 / 156 | 45.8 / 117 | 48.9 / 144 | 40.1 / 163 | **24.2 / 61** | 20.3 / 55 | 21.8 / 55 |
| aggregations | 173 / 281 | 168 / 203 | 157 / 251 | 164 / 237 | **58.7 / 90** | 61.3 / 100 | 59.8 / 85 |
| search_after | 21.6 / 41.7 | 15.8 / 50.9 | 19.1 / 37.5 | 21.0 / 33.2 | 12.8 / 19.8 | 12.3 / 18.8 | 11.9 / 17.9 |

**Throughput at saturation (c = 8), QPS**

| workload | 1 node, 1 shard | 1 node, 3 shards | cluster p1r0 | **cluster p1r2** | cluster p3r0 | cluster p3r1 | **cluster p3r2** |
|---|---|---|---|---|---|---|---|
| exact | 824 | 1,319 | 852 | **1,525** | 1,087 | 1,370 | 1,442 |
| bool_filter | 86 | 70 | 87 | **239** | 173 | 189 | 197 |
| fuzzy | 437 | 239 | 436 | **924** | 445 | 505 | 528 |
| full_text | 13.9 | 12.9 | 14.5 | 37.5 | 36.1 | 35.8 | **39.6** |
| **full_search** | 20.9 | 21.7 | 21.9 | 59.7 | 47.6 | 57.3 | **62.6** |
| aggregations | 6.3 | 6.5 | 6.5 | 17.3 | 18.7 | 19.0 | **19.0** |
| search_after | 49.5 | 46.9 | 52.7 | **138.5** | 124.2 | 127.3 | 127.6 |
| full_search p95 ms | 681 | 610 | 776 | 314 | 295 | 242 | **241** |

- **Three nodes give about 3× the throughput of one**: `full_search` 20.9 → 62.6 QPS with P95 681 → 241 ms,
  `full_text` 13.9 → 39.6, aggregations 6.3 → 19.0. Hypothesis 1 holds for the expensive queries.
- **On one node with one CPU, shard count does not matter** (1 vs 3 shards: 20.9 vs 21.7 `full_search` QPS). There
  is no idle core to parallelise onto, which confirms experiment 06 from the other side.
- **3 primaries give the lowest latency**: `full_search` P50 24 ms vs 40–64 ms for any 1-primary layout, and
  aggregations 59 vs 157–173 ms. Every query runs on three CPUs at once.
- **1 primary + 2 replicas gives the highest throughput for cheap queries**: `bool_filter` 239 QPS vs 173–197,
  `fuzzy` 924 vs 445–528. Each query uses one node and there is no per-shard overhead (the cost measured in
  experiment 06). For expensive queries p1r2 is close to p3r2 (`full_search` 59.7 vs 62.6).
- **Replicas on 3 primaries still help a little, mostly the tail**: `full_search` 47.6 → 57.3 → 62.6 QPS and P95
  295 → 241 ms from r0 to r2. With a copy of every shard on two or three nodes, adaptive replica selection can send
  each shard request to the least busy copy instead of a fixed node. Hypothesis 2 was too strong: replicas add
  some throughput even when shards already cover every node.

## Result: indexing with replicas

| load (1M docs) | docs/s | load | then | total until green |
|---|---|---|---|---|
| 1 node, 1 shard | 4,971 | 201 s | — | 201 s |
| 1 node, 3 shards | 5,361 | 187 s | — | 187 s |
| cluster, 1 shard (one node indexes) | 5,254 | 190 s | — | 190 s |
| cluster, 3 shards, no replica | 11,716 – 14,875 | 67–85 s | — | 67–85 s |
| cluster, 3 shards, **replica during the load** | 7,505 | 133 s | — | 133 s |
| cluster, 3 shards, **replica added after** | 14,875 | 67 s | + 20 s replica copy | **87 s** |

- Indexing scales with the nodes that hold primaries: 3 primaries on 3 nodes index 2.4–3× faster than 1.
  One shard only uses one node, however big the cluster.
- A replica during the load costs 1.5× (133 s vs 87 s). Adding it afterwards, from the finished segments, took
  20 s for 3 × 270 MB. Hypothesis 3 holds, with a smaller penalty than 2×: the replicas index on otherwise idle CPU
  of the other nodes. Growing replicas later is cheap too: p3 r0 → r1 took 14.9 s, p1 r0 → r2 (2 × 792 MB) 41.7 s.

## Result: node failure

All runs: `full_search`, c = 4, the index with 3 primaries. "Client errors" counts requests that failed or
returned partial results *after* the client's one retry on another node.

| scenario | client errors | cluster state | recovery |
|---|---|---|---|
| **A**: 1 replica, data node killed, back after 5 s | **0** (514 retried on another node) | yellow within 1 s; node rejoined after 37 s (Elasticsearch startup) | green **2 s** after the node rejoined: its shard copies were reused, nothing was copied |
| **B**: 1 replica, data node gone for 130 s | 1 (partial result at the moment of the kill) | yellow; after **60 s** the missing replicas were rebuilt on the two other nodes | green on 2 nodes 79 s after the kill (18.7 s copying 2 shards, ~540 MB); when the node came back, 2 shards moved back to it in 15.5 s |
| **C**: **no replica**, data node killed | **2,348 = every request for ~37 s** | **red** for ~36 s | green 1 s after the node rejoined |
| **D**: 1 replica, **master** killed | **0** (441 retried) | yellow after 2 s; new master `es02` elected | green 1.4 s after the node rejoined |

- **With one replica, losing any node, including the master, loses no request.** Replicas are promoted at once.
  Killing the master cost more than a data node: yellow after 2 s instead of 1 s, and throughput fell from ~36 to
  17 req/s with P95 506 ms for about a second while the other two nodes elected a new master.
- **The 60 s wait in B is `index.unassigned.node_left.delayed_timeout` (default 1m).** Elasticsearch waits a
  minute before copying the missing replicas, in case the node comes back. In A it came back within the minute
  (~37 s, most of it JVM startup), and recovery took 2 s instead of copying 270 MB per shard. In B it did not,
  and the cluster restored full redundancy on its own, then rebalanced when the node returned.
- **Without a replica (C), Elasticsearch does not fail the request: it returns HTTP 200 with the hits from the
  surviving shards**, and `_shards.failed: 1`. Hypothesis 4 was wrong on this point, and it mattered:
  `/search` ignored `_shards`, so it served these incomplete results as complete, and cached them. A direct check
  during the outage: a query matching 177,661 documents reported 118,685.
  **Fixed in this change**: `search.Result.Partial` is set when `_shards.failed > 0` or the search timed out;
  the API still serves the results but marks them `"partial": true`, logs a warning, counts
  `atlas_search_partial_results_total` and does not cache them.
- **A rejoining node is slow for a few seconds**: in A, C and D the per-second P95 reached ~1.9 s just after
  the node came back (cold JVM and caches).
- In B, throughput with two nodes (~43 req/s at c = 4) was close to three nodes (~45–51). The c = 4 load did not
  saturate the cluster, so these runs show availability and latency, not the capacity lost.

## Production implication

- **Run at least 1 replica.** A replica is what turns a node failure from 37 s of silently wrong answers (C) into
  no visible error (A, B, D). The cost is small: 2× disk, and 20 s to build after a bulk load.
- **Layout for this project: 3 primaries, 1 replica, 3 nodes.** It has the lowest latency (`/search` P50 20 ms,
  3× faster than one node) and near-best throughput, and it survives any one node. For read-heavy traffic of cheap
  queries, `1 primary + 2 replicas` gives up to 1.8× the throughput of cheap queries (`fuzzy` 924 vs 505 QPS) but 2–3× the latency of
  aggregations and `/search`; `3 primaries + 2 replicas` improves the tail a little more at 3× the disk.
- **Build replicas after bulk loads**: 87 s instead of 133 s for 1M documents. `datagen -replicas N` does this.
  `crawler -reindex` does the same: it loads the new index without replicas, adds the live index's replica count,
  waits for green and only then swaps the alias (it aborts, alias unchanged, if the replicas cannot be allocated).
- **Keep `delayed_timeout` above the node restart time.** Here a restart takes ~37 s against the 60 s default. For
  planned rolling restarts, set `cluster.routing.allocation.enable: primaries` first, so the cluster does not start
  copying data during the restart.
- **Clients need every node.** The benchmark survived each failure only because it knew all three nodes and retried
  on another one. The API is configured with one `ELASTICSEARCH_URL`: if that node dies, `/search` fails although
  the cluster is healthy. Next step: several URLs with retry in `search.Client` (or a load balancer in front).
- **Treat partial results as a signal**: alert on `atlas_search_partial_results_total`. With replicas it should stay
  at 0; any increase means a shard has no live copy.
