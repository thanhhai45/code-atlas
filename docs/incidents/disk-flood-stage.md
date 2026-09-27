# Writes rejected: disk flood stage

**Symptoms**: bulk writes fail with HTTP 429
`cluster_block_exception ... disk usage exceeded flood-stage watermark, index has read-only-allow-delete block`.
The crawler exits with "N of M repositories are in PostgreSQL but not in the search index"; `crawl_runs.status`
is `partial`. Searches still work, but return stale data.

**Check**

```bash
make diagnose     # "write-blocked indices" and each node's disk.used_percent / disk.avail
curl -s 'localhost:9200/_cluster/settings?include_defaults=true&filter_path=**.watermark*'
```

Defaults: low 85% (no new shards on the node), high 90% (shards move away), flood stage 95% (indices with a shard
on the node become read-only).

**Fix**

1. Free disk: delete old indices (`crawler -reindex -keep-old` leaves the previous one behind), heap dumps
   (`*.hprof` in the data directory, see [heap-oom.md](heap-oom.md)), or add disk or nodes.
2. Elasticsearch removes the block by itself once usage is below the high watermark (16 s in experiment 08). To
   remove it at once: `PUT /_all/_settings {"index.blocks.read_only_allow_delete": null}`.
3. Rebuild what the crawler could not index: `crawler -reindex` (PostgreSQL has every repository).

**Prevent**: alert on disk above the high watermark, long before the flood stage. Never disable
`cluster.routing.allocation.disk.threshold_enabled` outside local development.
