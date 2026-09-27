#!/usr/bin/env bash
# First look at an unhealthy Elasticsearch cluster: health, nodes (heap, disk,
# CPU), indices that are not green or are write-blocked, unassigned shards and
# why Elasticsearch will not allocate them, breaker trips and pending tasks.
# Read-only: it changes nothing. Runbooks: docs/incidents/.
#
#   scripts/cluster-diagnose.sh                       # ELASTICSEARCH_URL or localhost:9200
#   ELASTICSEARCH_URL=http://es1:9200,http://es2:9200 scripts/cluster-diagnose.sh
set -euo pipefail

IFS=, read -ra nodes <<<"${ELASTICSEARCH_URL:-http://localhost:9200}"
es=""
for u in "${nodes[@]}"; do
  if curl -fs -m 3 "$u" > /dev/null; then es=${u%/}; break; fi
done
[[ -n $es ]] || { echo "no Elasticsearch node reachable at ${ELASTICSEARCH_URL:-http://localhost:9200}" >&2; exit 1; }
get() { curl -s -m 20 "$es$1"; }

echo "== cluster (via $es)"
get "/_cluster/health" | python3 -c '
import json, sys
h = json.load(sys.stdin)
print("status {status}, nodes {number_of_nodes}, active shards {active_shards_percent_as_number:.0f}%, "
      "unassigned {unassigned_shards} (delayed {delayed_unassigned_shards}), initializing {initializing_shards}, "
      "relocating {relocating_shards}, pending tasks {number_of_pending_tasks}".format(**h))'

echo; echo "== nodes"
get "/_cat/nodes?v&h=name,master,node.role,heap.percent,ram.percent,cpu,load_1m,disk.used_percent,disk.avail,uptime&s=name"

echo; echo "== circuit breakers that tripped"
get "/_nodes/stats/breaker" | python3 -c '
import json, sys
found = False
for n in json.load(sys.stdin)["nodes"].values():
    for name, b in n["breakers"].items():
        if b["tripped"]:
            found = True
            print("%s: %s tripped %d times (limit %s, now %s)" % (n["name"], name, b["tripped"], b["limit_size"], b["estimated_size"]))
print("none" if not found else "", end="")'
echo

echo; echo "== indices not green"
out=$(get "/_cat/indices?h=health,index,pri,rep,docs.count,store.size&s=health" | grep -v '^green' || true)
echo "${out:-none}"

echo; echo "== write-blocked indices (disk flood stage or manual block)"
get "/_all/_settings/index.blocks*?flat_settings=true" | python3 -c '
import json, sys
blocked = [(i, k) for i, s in json.load(sys.stdin).items() for k, v in s["settings"].items() if v == "true"]
for i, k in blocked:
    print(f"{i}: {k}")
print("none" if not blocked else "", end="")'
echo

echo; echo "== unassigned shards"
unassigned=$(get "/_cat/shards?h=index,shard,prirep,state,unassigned.reason" | awk '$4 == "UNASSIGNED"')
if [[ -z $unassigned ]]; then
  echo "none"
else
  echo "$unassigned" | head -20
  # Explain the first few: the decider messages say exactly what blocks allocation.
  echo "$unassigned" | head -3 | while read -r index shard prirep _; do
    primary=false; [[ $prirep == p ]] && primary=true
    echo; echo "-- why is $index shard $shard ($prirep) unassigned?"
    curl -s -m 20 "$es/_cluster/allocation/explain" -H 'Content-Type: application/json' \
      -d "{\"index\":\"$index\",\"shard\":$shard,\"primary\":$primary}" | python3 -c '
import json, sys
e = json.load(sys.stdin)
if "error" in e:
    print(e["error"].get("reason")); sys.exit()
print(e.get("allocate_explanation") or e.get("explanation") or e.get("can_allocate"))
info = e.get("unassigned_info", {})
if info:
    print("  since %s, reason %s: %s" % (info.get("at"), info.get("reason"), info.get("details", "")[:200]))
seen = set()
for n in e.get("node_allocation_decisions", []):
    for d in n.get("deciders", []):
        if d["decision"] == "NO" and (d["decider"], d["explanation"]) not in seen:
            seen.add((d["decider"], d["explanation"]))
            print("  [%s] %s" % (d["decider"], d["explanation"][:300]))'
  done
fi

echo; echo "== pending cluster tasks"
out=$(get "/_cat/pending_tasks?h=insertOrder,timeInQueue,priority,source")
echo "${out:-none}"
