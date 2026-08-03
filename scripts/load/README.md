# Ingestion load & scale harness (ROAD_TO_100 · F0.2)

The performance-baseline harness for the telemetry **ingestion hot path** —
gateway → log-service queue → Kafka → Quickwit/ClickHouse. It drives every
log-ingest protocol PulseTrace accepts and records both gateway latency and
downstream back-pressure. Results are published to
[`PERF_BASELINE.md`](../../PERF_BASELINE.md).

## Files

| File | Role |
| --- | --- |
| `ingest-load.js` | k6 multi-protocol, arrival-rate load with per-protocol latency thresholds and a JSON summary. |
| `collect-infra-metrics.sh` | Samples Kafka lag, ClickHouse parts/merges, and container CPU/mem during a run. |
| `run-baseline.sh` | Orchestrator: waits for health, runs the sampler + k6, renders the baseline. |
| `render-baseline.mjs` | Merges the k6 + infra JSON into the `PERF_BASELINE.md` results block. |

## Quick start

```bash
docker compose up -d --build
scripts/load/run-baseline.sh                       # full multi-protocol baseline → PERF_BASELINE.md
PROTOCOLS=logs RATE=80 DURATION=30s scripts/load/run-baseline.sh   # native path only, quick
```

Run k6 standalone (no infra sampling / no baseline render):

```bash
PROTOCOLS=logs,otlp_logs,dd_logs,splunk RATE=120 DURATION=2m k6 run scripts/load/ingest-load.js
```

## Protocols

`logs` (native), `otlp_logs`, `dd_logs`, `splunk` are on by default — all four land
on the Kafka log path but exercise different gateway decoders. `otlp_metrics` is
opt-in (it forwards to the OTLP collector, a different downstream). See
[`PERF_BASELINE.md`](../../PERF_BASELINE.md) for endpoints, thresholds, and the
rate model (why the aggregate rate is split the way it is against the
`telemetry-ingest` limiter).

## In CI

- **Per-PR fast gate** — `ci.yml` → `load-test`: `PROTOCOLS=logs` @ 80 req/s, 30s.
- **Scheduled scale run** — `scale-baseline.yml`: the full profile for 2m, weekly +
  on-demand (`workflow_dispatch`), uploading the summary and `PERF_BASELINE.md`.
