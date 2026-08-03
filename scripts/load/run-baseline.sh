#!/usr/bin/env bash
# Ingestion performance-baseline runner (ROAD_TO_100 · F0.2).
#
# Orchestrates one baseline run against a *running* docker-compose stack:
#   1. waits for the gateway to be healthy,
#   2. starts the infra-metrics sampler in the background,
#   3. drives the multi-protocol k6 ingestion load,
#   4. merges k6 latency + infra back-pressure into PERF_BASELINE.md.
#
# It does NOT bring the stack up/down — that stays the caller's decision (CI or a
# developer), so the same script serves `docker compose up` locally and the
# scheduled CI job. Exit code is k6's, so a threshold breach fails the run.
#
# Usage:
#   docker compose up -d --build      # (or an already-running stack)
#   scripts/load/run-baseline.sh
#
# Env (with sensible defaults; forwarded to the sub-scripts):
#   BASE_URL, INGEST_KEY, RATE, DURATION, BATCH, PROTOCOLS   → k6
#   SAMPLES, INTERVAL                                        → infra sampler
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
cd "$ROOT"

BASE_URL="${BASE_URL:-http://localhost:8080}"
DURATION="${DURATION:-2m}"
RATE="${RATE:-120}"
BATCH="${BATCH:-50}"
PROTOCOLS="${PROTOCOLS:-logs,otlp_logs,dd_logs,splunk}"
SUMMARY_OUT="${SUMMARY_OUT:-scripts/load/summary.json}"
INFRA_OUT="${INFRA_OUT:-scripts/load/infra-metrics.json}"

# Size the infra sampler to cover the k6 window (parse DURATION's m/s suffix).
dur_seconds() {
  local d="$1"
  case "$d" in
    *m) echo $(( ${d%m} * 60 )) ;;
    *s) echo "${d%s}" ;;
    *)  echo "$d" ;;
  esac
}
WINDOW="$(dur_seconds "$DURATION")"
export INTERVAL="${INTERVAL:-5}"
export SAMPLES="${SAMPLES:-$(( WINDOW / INTERVAL ))}"
[[ "$SAMPLES" -lt 1 ]] && export SAMPLES=1

echo "==> waiting for gateway at $BASE_URL/healthz"
for i in $(seq 1 60); do
  if curl -fsS "$BASE_URL/healthz" >/dev/null 2>&1; then
    echo "    gateway healthy after ${i}s"; break
  fi
  [[ "$i" -eq 60 ]] && { echo "gateway not healthy in time" >&2; exit 1; }
  sleep 2
done

echo "==> starting infra sampler (${SAMPLES}×${INTERVAL}s) in background"
OUT="$INFRA_OUT" "$HERE/collect-infra-metrics.sh" &
COLLECTOR_PID=$!

echo "==> running k6 ($PROTOCOLS @ ${RATE} req/s aggregate, batch ${BATCH}, ${DURATION})"
set +e
BASE_URL="$BASE_URL" RATE="$RATE" DURATION="$DURATION" BATCH="$BATCH" \
  PROTOCOLS="$PROTOCOLS" SUMMARY_OUT="$SUMMARY_OUT" INGEST_KEY="${INGEST_KEY:-}" \
  k6 run "$HERE/ingest-load.js"
K6_RC=$?
set -e 2>/dev/null || true

# Let the sampler finish its window (it self-terminates after SAMPLES).
wait "$COLLECTOR_PID" 2>/dev/null || true

echo "==> rendering PERF_BASELINE.md"
if command -v node >/dev/null 2>&1 && [[ -f "$SUMMARY_OUT" ]]; then
  node "$HERE/render-baseline.mjs" "$SUMMARY_OUT" "$INFRA_OUT" || echo "render failed (non-fatal)" >&2
else
  echo "    skipped render (node missing or no k6 summary)" >&2
fi

echo "==> done (k6 exit ${K6_RC})"
exit "$K6_RC"
