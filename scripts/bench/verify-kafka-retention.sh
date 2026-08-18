#!/usr/bin/env bash
#
# Verifies that Kafka retention can actually reclaim disk on this broker.
#
# Why this is not just "read retention.hours back":
#
# Kafka only deletes *closed* segments. The stock defaults are a 1 GiB segment
# that rolls after 168h, and a 2 GiB corpus spread over 10 partitions puts
# ~215 MiB in each — a fifth of one segment. Every partition therefore holds a
# single perpetually-open segment, nothing is ever eligible for deletion, and
# lowering retention.hours changes nothing whatsoever. The setting reads as
# correct while the disk grows without bound; that is exactly how 4.32 GiB of
# Kafka accumulated against a 2 GiB ingest.
#
# So the check that means something is structural: are segments closing? A
# partition with one .log file has nothing collectable no matter what the
# retention policy says. A partition with several has all but the last eligible
# once they age out.
#
#   scripts/bench/verify-kafka-retention.sh [topic]
#
# Exit 1 if no segment has closed — the pre-fix pathology — or if the broker's
# settings could not be read.

set -euo pipefail

TOPIC="${1:-logs}"
BROKER="${KAFKA_CONTAINER:-pulsetrace-kafka}"
DATA_DIR="${KAFKA_DATA_DIR:-/var/lib/kafka/data}"

if ! docker ps --format '{{.Names}}' | grep -qx "$BROKER"; then
  echo "FATAL: broker container '$BROKER' is not running" >&2
  exit 1
fi

echo "==> Broker retention settings"
settings=$(docker exec "$BROKER" kafka-configs --bootstrap-server kafka:29092 \
  --describe --broker 1 --all 2>/dev/null \
  | grep -E "^  log\.(retention\.(hours|bytes)|roll\.hours|segment\.bytes|index\.size\.max\.bytes)=" \
  | sed -E 's/ sensitive=.*//; s/^  /    /' | sort) || true

if [ -z "$settings" ]; then
  echo "FATAL: could not read broker configuration" >&2
  exit 1
fi
echo "$settings"

echo
echo "==> Segment state for topic '$TOPIC'"

# One line per partition: "<partition> <segment-count> <bytes>".
per_partition=$(docker exec "$BROKER" sh -c "
  for d in ${DATA_DIR}/${TOPIC}-*; do
    [ -d \"\$d\" ] || continue
    n=\$(ls \"\$d\"/*.log 2>/dev/null | wc -l | tr -d ' ')
    b=\$(du -sb \"\$d\" 2>/dev/null | awk '{print \$1}')
    echo \"\$(basename \$d) \$n \$b\"
  done") || true

if [ -z "$per_partition" ]; then
  echo "FATAL: topic '$TOPIC' has no partition directories under ${DATA_DIR}" >&2
  exit 1
fi

partitions=0; segments=0; total=0; closed=0; multi=0
while read -r name n b; do
  [ -z "$name" ] && continue
  partitions=$((partitions + 1))
  segments=$((segments + n))
  total=$((total + b))
  # Every segment but the active one is closed and therefore collectable.
  if [ "$n" -gt 1 ]; then
    closed=$((closed + n - 1))
    multi=$((multi + 1))
  fi
done <<< "$per_partition"

printf '    partitions:        %d\n' "$partitions"
printf '    segment files:     %d\n' "$segments"
printf '    closed (eligible): %d across %d partitions\n' "$closed" "$multi"
printf '    bytes on disk:     %.2f GiB\n' "$(echo "$total/1073741824" | bc -l)"

echo
if [ "$closed" -eq 0 ]; then
  cat >&2 <<'MSG'
FAIL: every segment is still open, so retention can reclaim nothing.

This is the defect this check exists to catch, and it is invisible in the
retention setting itself. Either segment.bytes is larger than the data any
partition has accumulated, or roll.hours has not yet elapsed. Until a segment
closes, Kafka's disk usage grows monotonically no matter what retention says.

If the broker has only just started or very little has been produced, this may
simply be too early to tell — check again after roll.hours has passed or after
segment.bytes has been written to a partition.
MSG
  exit 1
fi

echo "PASS: $closed segment(s) closed and eligible for retention to reclaim."
echo "      Retention is now able to bound this topic's disk."
