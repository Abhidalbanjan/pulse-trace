#!/usr/bin/env bash
#
# Runs the query suite against both targets and rewrites the results block in
# BENCHMARK.md. The prose in that file is hand-written; everything between the
# BENCH:BEGIN/END markers is generated and must never be edited by hand — the
# same discipline PERF_BASELINE.md and PARITY_REPORT.md already follow.
#
# Assumes both stacks are already up and loaded with the same corpus:
#
#   go run ./scripts/bench/corpus --size-mb 10240 --seed 42 --out corpus.ndjson
#   docker compose up -d                                              # PulseTrace
#   docker compose -f scripts/bench/compose.openobserve.yml up -d      # OpenObserve
#   EXPECT_SHA256=<hash> ./scripts/bench/load.sh --target pulsetrace  --corpus corpus.ndjson
#   EXPECT_SHA256=<hash> ./scripts/bench/load.sh --target openobserve --corpus corpus.ndjson
#   ./scripts/bench/run-benchmark.sh --corpus corpus.ndjson --seed 42
#
# Env:
#   ITERATIONS   samples per query class (default 20)
#   BENCH_CPUS / BENCH_MEMORY   recorded into the report as the resource cap

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ITERATIONS="${ITERATIONS:-20}"
CORPUS=""
SEED="42"

while [ $# -gt 0 ]; do
  case "$1" in
    --corpus) CORPUS="$2"; shift 2 ;;
    --seed)   SEED="$2";   shift 2 ;;
    --iterations) ITERATIONS="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

PT_GATEWAY="${PT_GATEWAY:-http://127.0.0.1:8080}"
OO_URL="${OO_URL:-http://127.0.0.1:5080}"

echo "==> Checking both targets are reachable"
curl -sf "$PT_GATEWAY/healthz" >/dev/null || { echo "FATAL: PulseTrace not reachable at $PT_GATEWAY" >&2; exit 1; }
curl -sf "$OO_URL/healthz"     >/dev/null || { echo "FATAL: OpenObserve not reachable at $OO_URL" >&2; exit 1; }
echo "    both up"

echo "==> Collecting measurements (${ITERATIONS} iterations per query class)"
ITERATIONS="$ITERATIONS" CORPUS="$CORPUS" SEED="$SEED" \
PT_GATEWAY="$PT_GATEWAY" OO_URL="$OO_URL" \
BENCH_CPUS="${BENCH_CPUS:-4}" BENCH_MEMORY="${BENCH_MEMORY:-8g}" \
  node "$ROOT/scripts/bench/collect.mjs" > "$ROOT/scripts/bench/.last-run.json"

echo "==> Rendering BENCHMARK.md"
node "$ROOT/scripts/bench/write-report.mjs" "$ROOT/scripts/bench/.last-run.json" "$ROOT/BENCHMARK.md"

echo "==> Done. Results written to BENCHMARK.md"
