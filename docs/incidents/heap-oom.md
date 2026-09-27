# Node out of memory

**Symptoms**: a node disappears; its log ends with `java.lang.OutOfMemoryError: Java heap space` and
`Heap dump file created`; the cluster turns yellow (or red for indices without replicas). Before that: heap above
85% and long GC pauses (`attempting to trigger G1GC due to high heap usage`).

**Check**

```bash
make diagnose                               # heap.percent per node, tripped breakers
docker logs <node> 2>&1 | grep -E 'OutOfMemory|circuit|G1GC'
ls -la <data dir>/*.hprof                   # heap dumps
```

**Fix**

1. Restart the node. Shards recover from its disk (seconds) if it is back within `delayed_timeout`.
2. **Move or delete the heap dump.** It is written to the data directory (1.4 GB for a 1 GB heap in experiment 08)
   and can push the disk into the flood stage ([disk-flood-stage.md](disk-flood-stage.md)).
3. Find the request: large aggregations are the usual cause. Check that `search.max_buckets` is at its default
   (65,536): `GET /_cluster/settings?include_defaults=true&filter_path=**.max_buckets`.

**Prevent**: keep `search.max_buckets` at the default. In experiment 08 it rejected 500K- and 1M-bucket requests
cleanly; with it raised, four such requests at once killed a node before any circuit breaker tripped. Never let
clients choose aggregation sizes. Size the heap at no more than half the node's memory and below ~30 GB.
