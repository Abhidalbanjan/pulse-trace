#!/usr/bin/env bash
#
# Samples the physical footprint of each stack and prints it as JSON.
#
# Storage is measured as a *delta* around the load, not as an absolute. A raw
# total would charge PulseTrace for Postgres, Neo4j and Grafana baselines that
# have nothing to do with the corpus, and would flatter whichever side happens
# to ship fewer resident services. Delta answers the question the comparison is
# actually about: how much disk did these bytes cost.
#
#   scripts/bench/footprint.sh snapshot > before.json
#   ... load both targets ...
#   scripts/bench/footprint.sh snapshot > after.json
#   scripts/bench/footprint.sh delta before.json after.json > footprint.json
#
# RSS is sampled at call time from `docker stats`, so take the "after" snapshot
# while the stacks are still warm — a stack that has been idle for an hour has
# already given memory back and will look better than it was.
#
# ── Why this is the second implementation ────────────────────────────────────
#
# The first version identified each stack by matching a name prefix, and it was
# wrong twice, in opposite directions, both times in ways that were invisible
# in the output:
#
#   * It matched *volumes* against `pulse-trace_`. Images that declare `VOLUME`
#     in their Dockerfile (Kafka, ClickHouse, ZooKeeper, Neo4j, Redis, Jaeger,
#     Pyroscope) get an anonymous volume with a 64-hex name, which matches no
#     prefix and was silently skipped — hiding 4.86 GiB, 4.32 GiB of it Kafka,
#     while OpenObserve's two volumes are both named and were fully counted.
#     A footprint comparison that counts one side's disk and not the other's is
#     not a measurement, and this one was biased toward the side writing it.
#     It also counted a *stale* `pulse-trace_clickhouse_data` volume left by an
#     older compose revision: charged for disk nothing was writing to, while
#     missing the volume ClickHouse actually uses.
#
#   * It matched *containers* against `pulsetrace-`, which swept in an orphaned
#     `clickhouse-enterprise` container from a superseded compose revision and
#     reported 24 containers where the stack defines 23. Overstating our own
#     container count is the friendlier direction to be wrong in, but it is
#     still wrong, and a number nobody can reproduce from the compose file
#     discredits the ones next to it.
#
# So both are now derived from the compose file itself: a container counts only
# if its `com.docker.compose.service` label appears in `config --services`, and
# disk is whatever those containers actually have mounted.

set -euo pipefail

PT_FILTER="${PT_CONTAINER_FILTER:-pulsetrace-}"
OO_FILTER="${OO_CONTAINER_FILTER:-bench-oo}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PT_COMPOSE="${PT_COMPOSE_FILE:-${here}/../../docker-compose.yml}"
OO_COMPOSE="${OO_COMPOSE_FILE:-${here}/compose.openobserve.yml}"

# Running containers belonging to a stack, orphans excluded.
#
# The compose file is the definition of the stack; a container that merely
# shares a name prefix is not part of it. If the compose file cannot be read we
# fall back to the prefix and say so on stderr, because silently reverting to
# the buggy behaviour is how the original defect survived three runs.
stack_containers() {
  local filter="$1" compose_file="$2" services="" svc
  if [ -f "$compose_file" ]; then
    services=" $(docker compose -f "$compose_file" config --services 2>/dev/null | tr '\n' ' ') "
  fi
  if [ "$services" = "  " ] || [ -z "$services" ]; then
    echo "footprint: WARNING: cannot read ${compose_file}; counting by name prefix, orphans included" >&2
    docker ps --filter "name=${filter}" --format '{{.Names}}'
    return
  fi
  for c in $(docker ps --filter "name=${filter}" --format '{{.Names}}'); do
    svc=$(docker inspect "$c" --format '{{index .Config.Labels "com.docker.compose.service"}}' 2>/dev/null || true)
    case "$services" in
      *" ${svc} "*) echo "$c" ;;
    esac
  done
}

# Every distinct volume mounted by the stack's containers, named or anonymous.
# Deduplicated: two containers sharing a volume must not pay for it twice.
volume_bytes() {
  local total=0 n vols
  vols=$(for c in $(stack_containers "$1" "$2"); do
    docker inspect "$c" --format '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}{{println}}{{end}}{{end}}' 2>/dev/null
  done | grep -v '^$' | sort -u)
  for vol in $vols; do
    # du inside a throwaway container: the volume may be owned by another user
    # and is not necessarily reachable from the host filesystem.
    n=$(docker run --rm -v "${vol}:/v" alpine:3.20 du -sb /v 2>/dev/null | awk '{print $1}' || echo 0)
    total=$((total + ${n:-0}))
  done
  echo "$total"
}

# Bytes in container writable layers. A service that persists to a plain
# container path rather than a volume is not thereby free.
writable_layer_bytes() {
  local total=0 size n
  for c in $(stack_containers "$1" "$2"); do
    size=$(docker ps --size --filter "name=^/${c}$" --format '{{.Size}}' 2>/dev/null | sed -E 's/ \(virtual[^)]*\)//')
    n=$(awk -v s="$size" 'BEGIN{
      v = s + 0
      if (s ~ /GB/) v *= 1000000000
      else if (s ~ /MB/) v *= 1000000
      else if (s ~ /kB/) v *= 1000
      printf "%.0f", v
    }')
    total=$((total + ${n:-0}))
  done
  echo "$total"
}

container_count() {
  stack_containers "$1" "$2" | grep -c . | tr -d ' '
}

peak_rss() {
  # Sum of current RSS across the stack's containers. `docker stats --no-stream`
  # reports "123.4MiB / 8GiB"; take the left side.
  local total=0 names mem
  names=$(stack_containers "$1" "$2" | tr '\n' ' ')
  [ -z "${names// /}" ] && { echo 0; return; }
  # shellcheck disable=SC2086
  while read -r mem; do
    [ -z "$mem" ] && continue
    total=$(awk -v m="$mem" -v t="$total" 'BEGIN{
      v = m + 0
      if (m ~ /GiB|GB/) v *= 1073741824
      else if (m ~ /MiB|MB/) v *= 1048576
      else if (m ~ /KiB|kB/) v *= 1024
      else v = 0
      printf "%.0f", t + v
    }')
  done < <(docker stats --no-stream --format '{{.MemUsage}}' $names 2>/dev/null | cut -d'/' -f1)
  echo "$total"
}

case "${1:-}" in
  snapshot)
    cat <<EOF
{
  "pulsetrace": {
    "diskBytes": $(volume_bytes "$PT_FILTER" "$PT_COMPOSE"),
    "writableLayerBytes": $(writable_layer_bytes "$PT_FILTER" "$PT_COMPOSE"),
    "containers": $(container_count "$PT_FILTER" "$PT_COMPOSE"),
    "peakRssBytes": $(peak_rss "$PT_FILTER" "$PT_COMPOSE")
  },
  "openobserve": {
    "diskBytes": $(volume_bytes "$OO_FILTER" "$OO_COMPOSE"),
    "writableLayerBytes": $(writable_layer_bytes "$OO_FILTER" "$OO_COMPOSE"),
    "containers": $(container_count "$OO_FILTER" "$OO_COMPOSE"),
    "peakRssBytes": $(peak_rss "$OO_FILTER" "$OO_COMPOSE")
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
    b, a = before[side], after[side]
    def grew(key):
        return max(0, a.get(key, 0) - b.get(key, 0))
    # Volumes and writable layers are both disk the deployment occupies.
    total = grew("diskBytes") + grew("writableLayerBytes")
    out[side] = {
        "diskBytes": total,
        "volumeBytes": grew("diskBytes"),
        "writableLayerBytes": grew("writableLayerBytes"),
        # Normalised so the two sides are comparable regardless of corpus size.
        "bytesPerGiB": round(total / (ingested / 1024**3)) if ingested else None,
        "containers": a["containers"],
        "peakRssBytes": a["peakRssBytes"],
    }
print(json.dumps(out, indent=2))
PY
    ;;

  *)
    echo "usage: footprint.sh snapshot | delta <before.json> <after.json>" >&2
    exit 2
    ;;
esac
