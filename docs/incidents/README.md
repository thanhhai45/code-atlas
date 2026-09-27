# Incident runbooks

Start with `make diagnose` (`scripts/cluster-diagnose.sh`): a read-only report of cluster health, node heap and
disk, tripped breakers, red/yellow and write-blocked indices, and why unassigned shards are unassigned. Then open
the runbook for the symptom. Each one comes from a failure reproduced in
[experiment 07](../experiments/07-cluster.md) or [experiment 08](../experiments/08-failure-lab.md).

| symptom | runbook |
|---|---|
| Cluster red or yellow, `"partial": true` in `/search` responses | [unassigned-shards.md](unassigned-shards.md) |
| Writes fail with 429 `cluster_block_exception`, crawler exits with "not in the search index" | [disk-flood-stage.md](disk-flood-stage.md) |
| Search latency up for everyone, CPU saturated | [slow-search.md](slow-search.md) |
| A node died with `OutOfMemoryError`, heap near 100% | [heap-oom.md](heap-oom.md) |
| A node is down | Nothing to do right away if indices have replicas: requests fail over (experiment 07). Bring it back within `index.unassigned.node_left.delayed_timeout` (1 min) to avoid copying its shards again. |
