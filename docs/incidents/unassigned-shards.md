# Cluster red or yellow (unassigned shards)

**Symptoms**: `/health` reports `red` or `yellow`; `/search` responses carry `"partial": true` and
`atlas_search_partial_results_total` rises (red: a primary has no live copy, so its documents are missing from
results). Yellow alone is invisible to users but means one more failure loses data.

**Check**

```bash
make diagnose                      # unassigned shards and the deciders that block them
curl -s localhost:9200/_cluster/allocation/explain?pretty   # the first unassigned shard in full
```

**Common causes, as the explain API words them**

| decider says | cause | fix |
|---|---|---|
| `[same_shard] a copy of this shard is already allocated to this node` | more replicas than nodes − 1 | lower `number_of_replicas` or add nodes |
| `[filter] node does not match index setting [index.routing.allocation.require]` | an allocation filter no node satisfies | fix or remove the filter setting |
| `[disk_threshold] ... exceeded the high watermark` | disk full on the candidate nodes | [disk-flood-stage.md](disk-flood-stage.md) |
| `[max_retry] shard has exceeded the maximum number of retries` | allocation failed 5 times (e.g. a corrupt file, a full disk at the time) | fix the cause, then `POST /_cluster/reroute?retry_failed=true` |
| primary unassigned with reason `NODE_LEFT` | the node holding the only copy is gone | bring the node back; with no replica there is no other copy |

A `NODE_LEFT` replica that is not yet allocated is normal for up to 1 minute (`delayed_timeout`): Elasticsearch
waits for the node to return before copying the data again.

**Prevent**: run every index with at least one replica (experiment 07). `crawler -reindex` keeps the live index's
replica count and refuses to swap until the new index is green.
