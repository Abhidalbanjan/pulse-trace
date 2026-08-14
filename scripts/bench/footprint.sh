#!/usr/bin/env bash
#
# Samples the physical footprint of each stack and prints it as JSON.
#
# Storage is measured as a *delta* around the load, not as an absolute. A raw
# total would charge PulseTrace for Postgres, Neo4j, Grafana and a week of Kafka
# retention that have nothing to do with the corpus, and would flatter whichever
# side happens to ship fewer resident services. Delta answers the question the
# comparison is actually about: how much disk did these bytes cost.
#
#   scripts/bench/footprint.sh snapshot > before.json
#   ... load both targets ...
#   scripts/bench/footprint.sh snapshot > after.json
#   scripts/bench/footprint.sh delta before.json after.json > footprint.json
#
# RSS is sampled at call time from `docker stats`, so take the "after" snapshot
# while the stacks are still warm — a stack that has been idle for an hour has
# already given memory back and will look better than it was.

set -euo pipefail

# Volumes are matched by compose project prefix so the two stacks never bleed
# into each other's numbers.
PT_PREFIX="${PT_VOLUME_PREFIX:-pulse-trace_}"
OO_PREFIX="${OO_VOLUME_PREFIX:-pulsetrace-bench-oo_}"

volume_bytes() {
  local prefix="$1" total=0
  for vol in $(docker volume ls --format '{{.Name}}' | grep "^${prefix}" || true); do
    # du inside a throwaway container: the volume may be owned by another user
    # and is not necessarily reachable from the host filesystem.
    local n
    n=$(docker run --rm -v "${vol}:/v" alpine:3.20 du -sb /v 2>/dev/null | awk '{print $1}' || echo 0)
    total=$((total + ${n:-0}))
  done
  echo "$total"
}

container_count() {
  docker ps --filter "name=$1" --format '{{.Names}}' | wc -l | tr -d ' '
}

peak_rss() {
  # Sum of current RSS across the stack's containers. `docker stats --no-stream`
  # reports "123.4MiB / 8GiB"; take the left side.
  local filter="$1" total=0
  while read -r mem; do
    [ -z "$mem" ] && continue
    local n unit
    n=$(echo "$mem" | sed -E 's/([0-9.]+).*/\1/')
    unit=$(echo "$mem" | sed -E 's/[0-9.]+([A-Za-z]+).*/\1/')
    case "$unit" in
      GiB|GB) n=$(echo "$n * 1073741824" | bc) ;;
      MiB|MB) n=$(echo "$n * 1048576" | bc) ;;
      KiB|KB) n=$(echo "$n * 1024" | bc) ;;
      *)      n=0 ;;
    esac
    total=$(echo "$total + $n" | bc)
  done < <(docker stats --no-stream --format '{{.Name}}\t{{.MemUsage}}' 2>/dev/null | grep "$filter" | cut -f2 | cut -d'/' -f1)
  printf '%.0f\n' "$total"
}

case "${1:-}" in
  snapshot)
    cat <<EOF
{
  "pulsetrace": {
    "diskBytes": $(volume_bytes "$PT_PREFIX"),
    "containers": $(container_count pulsetrace-),
    "peakRssBytes": $(peak_rss pulsetrace-)
  },
  "openobserve": {
    "diskBytes": $(volume_bytes "$OO_PREFIX"),
    "containers": $(container_count bench-oo),
    "peakRssBytes": $(peak_rss bench-oo)
  }
}
EOF
    ;;

  delta)
    before="${2:?before.json required}"
    after="${3:?after.json required}"
    ingested="${INGESTED_BYTES:-0}"
    python3 - "$before" "$after" "$ingested" <<'PY'
import json, sys
before, after, ingested = json.load(open(sys.argv[1])), json.load(open(sys.argv[2])), int(sys.argv[3])
out = {}
for side in ("pulsetrace", "openobserve"):
    grew = max(0, after[side]["diskBytes"] - before[side]["diskBytes"])
    out[side] = {
        "diskBytes": grew,
        # Normalised so the two sides are comparable regardless of corpus size.
        "bytesPerGiB": round(grew / (ingested / 1024**3)) if ingested else None,
        "containers": after[side]["containers"],
        "peakRssBytes": after[side]["peakRssBytes"],
    }
print(json.dumps(out, indent=2))
PY
    ;;

  *)
    echo "usage: footprint.sh snapshot | delta <before.json> <after.json>" >&2
    exit 2
    ;;
esac
