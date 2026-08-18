#!/usr/bin/env bash
#
# Regression test for footprint.sh, driven by a stub `docker` on PATH.
#
# This exists because footprint.sh produced the headline storage number in
# BENCHMARK.md and got it wrong three runs in a row, silently. Every assertion
# below corresponds to a defect that shipped:
#
#   1. Anonymous volumes were skipped. Images declaring VOLUME in their
#      Dockerfile get a 64-hex volume name matching no compose prefix, so
#      Kafka's 4.32 GiB was invisible while OpenObserve — whose volumes are all
#      named — was counted in full. The bias favoured us.
#   2. Orphaned containers were counted. A container from a superseded compose
#      revision still carries the project label and matched the name filter,
#      inflating our own container count.
#   3. Writable-layer bytes always parsed as zero. The `sed` stripping the
#      "(virtual ...)" suffix was anchored greedily from the start of the line
#      and deleted the whole line.
#
# A stub is used rather than a live stack because these are parsing and
# selection bugs: they reproduce deterministically against canned docker output
# and would otherwise need a 40-minute ingest to observe.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail=0
check() {
  local label="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    echo "  ok    ${label}"
  else
    echo "  FAIL  ${label}: expected ${expected}, got ${actual}"
    fail=1
  fi
}

# ── Fixture ──────────────────────────────────────────────────────────────────
#
# Three real containers plus one orphan. Kafka and ClickHouse sit on anonymous
# volumes; ClickHouse also shares Quickwit's named volume, so dedup is exercised
# too. The orphan is deliberately the largest thing in the fixture: if it leaks
# into the totals, every assertion moves.

cat > "$tmp/compose.yml" <<'YAML'
services:
  kafka: {}
  quickwit: {}
  clickhouse: {}
YAML

cat > "$tmp/docker" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
args="$*"

case "$args" in
  "compose -f "*" config --services")
    printf 'kafka\nquickwit\nclickhouse\n' ;;

  "ps --size --filter name=^/"*)
    name="${args#*name=^/}"; name="${name%%\$*}"
    case "$name" in
      pulsetrace-kafka)      echo "16.4kB (virtual 876MB)" ;;
      pulsetrace-quickwit)   echo "4.1kB (virtual 200MB)" ;;
      pulsetrace-clickhouse) echo "69.5MB (virtual 907MB)" ;;
      pulsetrace-legacy)     echo "260MB (virtual 1.1GB)" ;;
      *) echo "0B (virtual 0B)" ;;
    esac ;;

  "ps --filter name=no-such-container- --format {{.Names}}")
    : ;;  # the OO side of the fixture: no containers, exercises the empty path

  "ps --filter name=pulsetrace- --format {{.Names}}")
    printf 'pulsetrace-kafka\npulsetrace-quickwit\npulsetrace-clickhouse\npulsetrace-legacy\n' ;;

  "inspect "*"Config.Labels"*)
    set -- $args
    case "$2" in
      pulsetrace-kafka)      echo "kafka" ;;
      pulsetrace-quickwit)   echo "quickwit" ;;
      pulsetrace-clickhouse) echo "clickhouse" ;;
      pulsetrace-legacy)     echo "legacy" ;;   # orphan: absent from compose.yml
    esac ;;

  "inspect "*".Mounts"*)
    set -- $args
    case "$2" in
      # 64-hex anonymous volume, as created by an image VOLUME directive.
      pulsetrace-kafka)      echo "ceff0a349f01992c3d57df2679be7882f149a82b5cfe0d8334306d01ed9f9c88" ;;
      pulsetrace-quickwit)   echo "pulse-trace_quickwit_data" ;;
      # Anonymous volume, plus one shared with quickwit to exercise dedup.
      pulsetrace-clickhouse) printf '47eb7f437a612066c0d5aa4955e56a4773bd09fbca24e8379fd22fd2eab73091\npulse-trace_quickwit_data\n' ;;
      pulsetrace-legacy)     echo "pulse-trace_legacy_data" ;;
    esac ;;

  "run --rm -v "*)
    vol="${args#*-v }"; vol="${vol%%:*}"
    case "$vol" in
      ceff0a349f01992c3d57df2679be7882f149a82b5cfe0d8334306d01ed9f9c88) echo "4000000	/v" ;;
      47eb7f437a612066c0d5aa4955e56a4773bd09fbca24e8379fd22fd2eab73091) echo  "500000	/v" ;;
      pulse-trace_quickwit_data)                                        echo "1000000	/v" ;;
      pulse-trace_legacy_data)                                          echo "9000000	/v" ;;
      *) echo "0	/v" ;;
    esac ;;

  "stats --no-stream --format {{.MemUsage}} "*)
    for n in $(echo "$args" | sed 's/.*{{.MemUsage}} //'); do
      case "$n" in
        pulsetrace-kafka)      echo "500MiB / 8GiB" ;;
        pulsetrace-quickwit)   echo "1GiB / 8GiB" ;;
        pulsetrace-clickhouse) echo "250MiB / 8GiB" ;;
        pulsetrace-legacy)     echo "2GiB / 8GiB" ;;
      esac
    done ;;

  *) echo "STUB: unhandled: $args" >&2; exit 90 ;;
esac
STUB
chmod +x "$tmp/docker"

# The OpenObserve side is not exercised here; point it at an absent compose file
# so the fallback path is covered too (it warns and returns nothing).
out=$(PATH="$tmp:$PATH" \
     PT_CONTAINER_FILTER="pulsetrace-" \
     OO_CONTAINER_FILTER="no-such-container-" \
     PT_COMPOSE_FILE="$tmp/compose.yml" \
     OO_COMPOSE_FILE="$tmp/compose.yml" \
     "$here/footprint.sh" snapshot)

get() { echo "$out" | python3 -c "import sys,json;print(json.load(sys.stdin)['pulsetrace']['$1'])"; }

echo "footprint.sh"

# 4,000,000 (kafka, anonymous) + 500,000 (clickhouse, anonymous)
#   + 1,000,000 (quickwit, named — counted once despite two mounts)
#   = 5,500,000. The orphan's 9,000,000 must not appear.
check "counts anonymous volumes and dedups shared ones" 5500000 "$(get diskBytes)"

# 16.4kB + 4.1kB + 69.5MB, orphan's 260MB excluded. Zero here means the
# "(virtual ...)" strip ate the line again.
check "parses writable-layer sizes"                     69520500 "$(get writableLayerBytes)"

# Three compose services; the orphan carries the project label but no service.
check "excludes orphaned containers from the count"            3 "$(get containers)"

# 500MiB + 1GiB + 250MiB, orphan's 2GiB excluded.
check "sums RSS over stack containers only"           1860173824 "$(get peakRssBytes)"

# ── delta ────────────────────────────────────────────────────────────────────
# Volumes and writable layers are both real disk; the delta must add them, and
# must never report a negative when a store compacts during the run.
cat > "$tmp/before.json" <<'JSON'
{"pulsetrace":{"diskBytes":1000,"writableLayerBytes":500,"containers":3,"peakRssBytes":10},
 "openobserve":{"diskBytes":9000,"writableLayerBytes":100,"containers":2,"peakRssBytes":20}}
JSON
cat > "$tmp/after.json" <<'JSON'
{"pulsetrace":{"diskBytes":3000,"writableLayerBytes":900,"containers":3,"peakRssBytes":10},
 "openobserve":{"diskBytes":4000,"writableLayerBytes":100,"containers":2,"peakRssBytes":20}}
JSON
d=$(INGESTED_BYTES=$((1024*1024*1024)) "$here/footprint.sh" delta "$tmp/before.json" "$tmp/after.json")
dget() { echo "$d" | python3 -c "import sys,json;print(json.load(sys.stdin)['$1']['$2'])"; }

check "delta adds volume and writable-layer growth"  2400 "$(dget pulsetrace diskBytes)"
check "delta normalises per GiB ingested"            2400 "$(dget pulsetrace bytesPerGiB)"
check "delta floors shrinkage at zero"                  0 "$(dget openobserve diskBytes)"

[ "$fail" -eq 0 ] && echo "all passed" || echo "FAILURES"
exit "$fail"
