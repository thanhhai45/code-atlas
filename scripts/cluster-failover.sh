#!/usr/bin/env bash
# Kill one Elasticsearch node under search load and record what the cluster
# and the clients see (roadmap Phases 12-13).
#
#   scripts/cluster-failover.sh -a repositories_bench -n es03 -k 20 -r 100 -d 180 -o out/
#
# Runs cmd/bench against every node (round-robin, retry on another node) with a
# 1 s timeline, and polls _cluster/health every second into health.tsv. At -k
# seconds it kills the node's container (SIGKILL: no clean shutdown), at -r
# seconds it starts it again. Needs the cluster from infrastructure/cluster.
set -euo pipefail

alias=repositories_bench node=es03 kill_at=20 restart_at=100 duration=180
workload=full_search conc=4 out=failover urls=http://localhost:9200,http://localhost:9201,http://localhost:9202
project=${COMPOSE_PROJECT:-cluster}
while getopts "a:n:k:r:d:w:c:o:u:" opt; do
  case $opt in
    a) alias=$OPTARG ;; n) node=$OPTARG ;; k) kill_at=$OPTARG ;; r) restart_at=$OPTARG ;;
    d) duration=$OPTARG ;; w) workload=$OPTARG ;; c) conc=$OPTARG ;; o) out=$OPTARG ;; u) urls=$OPTARG ;;
    *) echo "usage: $0 [-a alias] [-n node] [-k kill_at_s] [-r restart_at_s] [-d duration_s] [-w workload] [-c concurrency] [-o dir] [-u urls]" >&2; exit 2 ;;
  esac
done
container="${project}-${node}-1"
mkdir -p "$out"
IFS=, read -ra nodes <<<"$urls"

# Ask any live node, so the poller survives the node it would otherwise ask.
health() {
  for u in "${nodes[@]}"; do
    if h=$(curl -fs -m 2 "$u/_cluster/health"); then echo "$h"; return; fi
  done
  echo '{}'
}

go run ./cmd/bench -url "$urls" -alias "$alias" -workloads "$workload" -c "$conc" \
  -d "${duration}s" -warmup 0 -timeline 1s -max-error-rate 1 -json "$out/bench.json" > "$out/bench.txt" 2>&1 &
bench_pid=$!
start=$(date +%s.%N)

echo -e "t_s\tevent\tstatus\tnodes\tactive_primary\tactive\trelocating\tinitializing\tunassigned" > "$out/health.tsv"
killed=0 restarted=0
while kill -0 "$bench_pid" 2>/dev/null; do
  t=$(echo "$(date +%s.%N) - $start" | bc)
  event=""
  if [[ $killed == 0 ]] && (( $(echo "$t >= $kill_at" | bc) )); then
    docker kill "$container" > /dev/null; killed=1; event="kill $node"
  elif [[ $restarted == 0 && $killed == 1 ]] && (( $(echo "$t >= $restart_at" | bc) )); then
    docker start "$container" > /dev/null; restarted=1; event="start $node"
  fi
  health | python3 -c '
import json, sys
t, event = sys.argv[1], sys.argv[2]
h = json.load(sys.stdin)
keys = ["status", "number_of_nodes", "active_primary_shards", "active_shards", "relocating_shards", "initializing_shards", "unassigned_shards"]
print("\t".join([f"{float(t):.1f}", event or "-"] + [str(h.get(k, "?")) for k in keys]))' "$t" "$event" >> "$out/health.tsv"
  sleep 1
done
wait "$bench_pid" || true
[[ $restarted == 1 ]] || docker start "$container" > /dev/null
echo "results in $out/ (bench.txt, bench.json, health.tsv)"
