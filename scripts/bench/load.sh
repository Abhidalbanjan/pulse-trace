#!/usr/bin/env bash
#
# Replays the benchmark corpus into one target over its own native ingest API,
# and reports wall-clock and achieved throughput.
#
# Each side gets its *native* path on purpose. Forcing OpenObserve to accept
# OTLP, or PulseTrace to accept _json, would measure a translation layer rather
# than the engine — and would quietly hand the advantage to whichever product
# the chosen wire format belongs to.
#
#   PulseTrace   POST /api/v1/logs        (native batch, bearer auth)
#   OpenObserve  POST /api/{org}/{stream}/_json
#
# Usage:
#   scripts/bench/load.sh --target pulsetrace --corpus corpus.ndjson
#   scripts/bench/load.sh --target openobserve --corpus corpus.ndjson
#
# Env:
#   PT_GATEWAY    default http://127.0.0.1:8080
#   PT_USER/PASS  default admin/admin
#   OO_URL        default http://127.0.0.1:5080
#   OO_USER/PASS  default bench@pulsetrace.local/benchpassword123
#   BATCH         records per request (default 500)

set -euo pipefail

TARGET=""
CORPUS=""
BATCH="${BATCH:-500}"

while [ $# -gt 0 ]; do
  case "$1" in
    --target) TARGET="$2"; shift 2 ;;
    --corpus) CORPUS="$2"; shift 2 ;;
    --batch)  BATCH="$2";  shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[ -n "$TARGET" ] || { echo "--target is required (pulsetrace|openobserve)" >&2; exit 2; }
[ -n "$CORPUS" ] || { echo "--corpus is required" >&2; exit 2; }
[ -r "$CORPUS" ] || { echo "corpus not readable: $CORPUS" >&2; exit 1; }

PT_GATEWAY="${PT_GATEWAY:-http://127.0.0.1:8080}"
PT_USER="${PT_USER:-admin}"
PT_PASS="${PT_PASS:-admin}"
OO_URL="${OO_URL:-http://127.0.0.1:5080}"
OO_USER="${OO_USER:-bench@pulsetrace.local}"
OO_PASS="${OO_PASS:-benchpassword123}"

corpus_bytes=$(wc -c < "$CORPUS" | tr -d ' ')
corpus_lines=$(wc -l < "$CORPUS" | tr -d ' ')
echo "corpus: $CORPUS  ${corpus_bytes} bytes  ${corpus_lines} records  batch=${BATCH}"

# Fail loudly on a corpus mismatch rather than silently benchmarking a different
# dataset than the one recorded alongside the results.
if [ -n "${EXPECT_SHA256:-}" ]; then
  actual=$(shasum -a 256 "$CORPUS" | awk '{print $1}')
  if [ "$actual" != "$EXPECT_SHA256" ]; then
    echo "FATAL: corpus sha256 mismatch" >&2
    echo "  expected $EXPECT_SHA256" >&2
    echo "  actual   $actual" >&2
    exit 1
  fi
  echo "corpus sha256 verified"
fi

start_epoch=$(date +%s)

case "$TARGET" in
  pulsetrace)
    token=$(curl -sf -X POST "$PT_GATEWAY/api/v1/auth/login" \
      -H 'Content-Type: application/json' \
      -d "{\"username\":\"$PT_USER\",\"password\":\"$PT_PASS\"}" \
      | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')
    [ -n "$token" ] || { echo "FATAL: could not authenticate to PulseTrace" >&2; exit 1; }

    # Only log records go down the native log path; traces and metrics have
    # their own endpoints and are measured separately by the query suite.
    python3 - "$CORPUS" "$BATCH" "$PT_GATEWAY" "$token" <<'PY'
import json, sys, urllib.request
corpus, batch, gateway, token = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
sent = failed = 0
buf = []

def flush():
    global sent, failed, buf
    if not buf:
        return
    body = json.dumps(buf).encode()
    req = urllib.request.Request(f"{gateway}/api/v1/logs", data=body, method="POST",
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {token}"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            if r.status < 300:
                sent += len(buf)
            else:
                failed += len(buf)
    except Exception:
        failed += len(buf)
    buf = []

with open(corpus) as f:
    for line in f:
        rec = json.loads(line)
        if rec.get("signal") != "log":
            continue
        buf.append({
            "service": rec["service"],
            "level": rec["level"],
            "message": rec["message"],
            "trace_id": rec.get("trace_id", ""),
            "metadata": rec.get("attrs", {}),
        })
        if len(buf) >= batch:
            flush()
flush()
print(f"  sent={sent} failed={failed}")
PY
    ;;

  openobserve)
    python3 - "$CORPUS" "$BATCH" "$OO_URL" "$OO_USER" "$OO_PASS" <<'PY'
import base64, json, sys, urllib.request
corpus, batch, url, user, password = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4], sys.argv[5]
auth = base64.b64encode(f"{user}:{password}".encode()).decode()
sent = failed = 0
buf = []

def flush():
    global sent, failed, buf
    if not buf:
        return
    body = json.dumps(buf).encode()
    req = urllib.request.Request(f"{url}/api/default/bench_logs/_json", data=body, method="POST",
        headers={"Content-Type": "application/json", "Authorization": f"Basic {auth}"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            if r.status < 300:
                sent += len(buf)
            else:
                failed += len(buf)
    except Exception:
        failed += len(buf)
    buf = []

with open(corpus) as f:
    for line in f:
        rec = json.loads(line)
        if rec.get("signal") != "log":
            continue
        flat = {
            "_timestamp": rec["timestamp"],
            "service": rec["service"],
            "level": rec["level"],
            "message": rec["message"],
            "trace_id": rec.get("trace_id", ""),
        }
        flat.update(rec.get("attrs", {}))
        buf.append(flat)
        if len(buf) >= batch:
            flush()
flush()
print(f"  sent={sent} failed={failed}")
PY
    ;;

  *)
    echo "unknown target: $TARGET (expected pulsetrace|openobserve)" >&2
    exit 2
    ;;
esac

elapsed=$(( $(date +%s) - start_epoch ))
[ "$elapsed" -gt 0 ] || elapsed=1
echo "target=$TARGET elapsed=${elapsed}s throughput=$(( corpus_bytes / elapsed / 1024 )) KiB/s"
