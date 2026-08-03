#!/usr/bin/env bash
# Infra-metrics sampler for the ingestion load test (ROAD_TO_100 · F0.2).
#
# Samples the downstream of the ingestion hot path while a load test runs, so the
# published baseline reflects not just gateway HTTP latency but back-pressure:
#   • Kafka consumer-group lag  — is the log topic draining as fast as it fills?
#   • ClickHouse parts/merges   — insert/merge pressure on the columnar store.
#   • Container CPU/mem          — headroom on gateway + log-service.
#
# It shells into the docker-compose containers (no host-side kafka/clickhouse
# client needed) and is tolerant: a missing container or tool degrades that one
# signal to "n/a" rather than aborting the run.
#
# Usage (run concurrently with k6, e.g. from run-baseline.sh):
#   scripts/load/collect-infra-metrics.sh
#
# Env:
#   SAMPLES        number of samples            (default 12)
#   INTERVAL       seconds between samples      (default 5)   → 12×5s = 60s window
#   OUT            output JSON path             (default scripts/load/infra-metrics.json)
#   KAFKA_CT       kafka container name         (default pulsetrace-kafka)
#   CH_CT          clickhouse container name    (default pulsetrace-clickhouse)
#   CH_DB          clickhouse database          (default pulsetrace)
#   CH_USER/CH_PW  clickhouse creds             (default pulsetrace / pulsetrace_secret)
#   STAT_CTS       space-separated containers to sample CPU/mem for
set -uo pipefail

SAMPLES="${SAMPLES:-12}"
INTERVAL="${INTERVAL:-5}"
OUT="${OUT:-scripts/load/infra-metrics.json}"
KAFKA_CT="${KAFKA_CT:-pulsetrace-kafka}"
CH_CT="${CH_CT:-pulsetrace-clickhouse}"
CH_DB="${CH_DB:-pulsetrace}"
CH_USER="${CH_USER:-pulsetrace}"
CH_PW="${CH_PW:-pulsetrace_secret}"
STAT_CTS="${STAT_CTS:-pulsetrace-gateway pulsetrace-log-service}"

have() { command -v "$1" >/dev/null 2>&1; }

if ! have docker; then
  echo "collect-infra-metrics: docker not found; writing empty metrics" >&2
  echo '{"error":"docker not available","samples":[]}' >"$OUT"
  exit 0
fi

container_up() { docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$1"; }

# Total consumer-group lag across all groups/partitions for the given topic.
# Discovers groups dynamically (Quickwit's native Kafka source group id is not
# fixed), so we don't hardcode a name that can drift.
kafka_total_lag() {
  container_up "$KAFKA_CT" || { echo "null"; return; }
  local out
  out="$(docker exec "$KAFKA_CT" kafka-consumer-groups \
    --bootstrap-server localhost:29092 --describe --all-groups 2>/dev/null)" || { echo "null"; return; }
  # LAG is column 6 in the --describe output; sum the numeric values.
  awk 'NR>1 && $6 ~ /^[0-9]+$/ {sum+=$6} END {if (NR>1) print sum+0; else print "null"}' <<<"$out"
}

# ClickHouse: active parts, total rows, ongoing merges on the logs table family.
ch_query() {
  container_up "$CH_CT" || { echo ""; return; }
  docker exec "$CH_CT" clickhouse-client --user "$CH_USER" --password "$CH_PW" \
    --database "$CH_DB" --query "$1" 2>/dev/null
}

ch_active_parts() {
  local v
  v="$(ch_query "SELECT count() FROM system.parts WHERE active AND database='${CH_DB}'")"
  [[ "$v" =~ ^[0-9]+$ ]] && echo "$v" || echo "null"
}
ch_rows() {
  local v
  v="$(ch_query "SELECT sum(rows) FROM system.parts WHERE active AND database='${CH_DB}'")"
  [[ "$v" =~ ^[0-9]+$ ]] && echo "$v" || echo "null"
}
ch_merges() {
  local v
  v="$(ch_query "SELECT count() FROM system.merges")"
  [[ "$v" =~ ^[0-9]+$ ]] && echo "$v" || echo "null"
}

# CPU% and mem (MiB) for a container from docker stats (single non-streaming read).
cpu_mem() {
  local ct="$1" line
  container_up "$ct" || { echo "null null"; return; }
  line="$(docker stats --no-stream --format '{{.CPUPerc}} {{.MemUsage}}' "$ct" 2>/dev/null)" || { echo "null null"; return; }
  local cpu mem
  cpu="$(awk '{gsub(/%/,"",$1); print $1}' <<<"$line")"
  mem="$(awk '{print $2}' <<<"$line" | sed 's/MiB.*//; s/GiB.*/*1024/' | bc 2>/dev/null)"
  [[ -n "$cpu" ]] || cpu="null"
  [[ -n "$mem" ]] || mem="null"
  echo "$cpu $mem"
}

echo "collect-infra-metrics: ${SAMPLES} samples × ${INTERVAL}s → $OUT" >&2

samples_json="[]"
for i in $(seq 1 "$SAMPLES"); do
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  lag="$(kafka_total_lag)"
  parts="$(ch_active_parts)"
  rows="$(ch_rows)"
  merges="$(ch_merges)"

  stats_json="{}"
  for ct in $STAT_CTS; do
    read -r cpu mem <<<"$(cpu_mem "$ct")"
    stats_json="$(jq -c --arg ct "$ct" --argjson cpu "${cpu:-null}" --argjson mem "${mem:-null}" \
      '. + {($ct): {cpuPct: $cpu, memMiB: $mem}}' <<<"$stats_json")"
  done

  sample="$(jq -cn \
    --arg ts "$ts" \
    --argjson lag "${lag:-null}" \
    --argjson parts "${parts:-null}" \
    --argjson rows "${rows:-null}" \
    --argjson merges "${merges:-null}" \
    --argjson stats "$stats_json" \
    '{ts:$ts, kafkaTotalLag:$lag, chActiveParts:$parts, chRows:$rows, chMerges:$merges, containers:$stats}')"
  samples_json="$(jq -c ". + [${sample}]" <<<"$samples_json")"

  printf 'infra sample %2d/%s  lag=%s parts=%s merges=%s\n' "$i" "$SAMPLES" "$lag" "$parts" "$merges" >&2
  [[ "$i" -lt "$SAMPLES" ]] && sleep "$INTERVAL"
done

# Roll up: peak lag/parts and peak CPU/mem per container — the numbers the
# baseline cares about (worst-case back-pressure during the run).
jq -n --argjson s "$samples_json" '
  def nums(f): [ $s[] | f | select(type=="number") ];
  {
    window: {samples: ($s|length)},
    kafka:  { peakTotalLag: (nums(.kafkaTotalLag) | max ), endTotalLag: ($s[-1].kafkaTotalLag // null) },
    clickhouse: { peakActiveParts: (nums(.chActiveParts)|max), peakMerges: (nums(.chMerges)|max), endRows: ($s[-1].chRows // null) },
    containers: (
      [ $s[].containers | keys[] ] | unique
      | map({ (.): {
          peakCpuPct: ([ $s[].containers[.].cpuPct | select(type=="number") ] | max),
          peakMemMiB: ([ $s[].containers[.].memMiB | select(type=="number") ] | max)
        }}) | add // {}
    ),
    samples: $s
  }' >"$OUT"

echo "collect-infra-metrics: wrote $OUT" >&2
