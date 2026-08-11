# Synthetic Monitoring — Implementation Plan

Spec: [../synthetics.md](../synthetics.md) · Service: **gateway** (worker + ClickHouse `synthetic_results`, Postgres `synthetic_targets`) · View: `frontend/src/components/Synthetics/SyntheticsView.tsx`

## Current state (grounded)
- Multi-step checks + assertion builder, pure `evaluateAssertion`, worker (stops at first failure), edge-triggered paging via the logs→alert pipeline, latency sparkline (F10). `synthetic_targets(name, spec JSONB)`, results cols incl. `CheckName`, `FailureReason`.

## E2 — Uptime / SLA timeline · M  *(recommended first slice)*
- `GET /api/v1/synthetics/uptime?target=&from=&to=` → uptime % + red/green availability strip from `synthetic_results`. Pure `computeUptime(results)`. FE availability timeline + SLA tiles. Parity: route consumed.
- Tests: `computeUptime`; e2e timeline renders.

## E3 — SSL cert & domain expiry · S
- Probe captures TLS/domain expiry; warn N days ahead (edge-triggered like failures). Pure `expiryStatus(notAfter, now, warnDays)`. FE expiry column.

## E1 — Multi-region probing · L
- Add a `location` dimension to results; run checks per configured region (region-tagged worker or location loop). FE region selector + per-region status/latency.

## E6 — Scheduling & maintenance windows · S
- Per-check interval + maintenance windows (reuse Alerts silences model); worker/notifier honor them. FE controls.

## E5 — Public status page · M
- Public tenant-scoped read endpoint (by slug, no auth) → components/uptime/incidents; standalone status page + Settings config. Parity: uiNone (public).

## E4 — Browser (headless) checks · L
- Playwright-based browser-check runner + artifact storage (object store); FE browser-step builder + failure screenshot.

## Sequencing & gates
E2 → E3 → E1 → E6 → E5 → E4. Per slice: gateway build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(synthetics): …`.
