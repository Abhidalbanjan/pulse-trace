# Services — Implementation Plan

Spec: [../services.md](../services.md) · Service: **gateway** · Views: `frontend/src/components/Services/{ServicesView,ServiceDetailView}.tsx`

## Current state (grounded)
- Services list + detail stitching deployments/profiler/service info (`/api/v1/services`, `/api/v1/deployments`, `/api/v1/profiler/…`).

## E1 — RED golden-signals dashboard · M  *(recommended first slice)*
- `GET /api/v1/services/{name}/signals?range=` → request rate, error rate, latency p50/p95/p99 from `otel_traces` (via `queryScoped`). Pure `redAggregationSQL(range)`. FE RED chart row atop the detail view + `<DeployMarkers>` overlay (Deploy-Gates E1). Parity: route consumed.
- Tests: RED SQL builder (tenant-scoped, param-bound); e2e RED charts render.

## E2 — Health score · S
- Pure `healthScore(errorRate, latencyVsSLO, openIncidents) (score, band)`. Shown on list + detail; sortable column.

## E4 — Unified activity timeline · S
- Merge deployments + incidents + alerts for the service into one timeline; `GET /api/v1/services/{name}/activity`. FE timeline strip.

## E5 — SLO & error-budget tiles · S
- Surface the service's SLO attainment + budget (reuse SLO dashboard filtered by service). FE tiles.

## E3 — Dependencies in/out with health · M
- Reuse topology upstream/downstream + trace edge latency; FE dependency panel with health chips.

## E6 — Ownership metadata · S
- Pull the Catalog entry (team/Slack/repo/on-call) onto the page. FE ownership card.

## Sequencing & gates
E1 → E2 → E4 → E5 → E3 → E6. Per slice: gateway build/vet/test (ratchet), FE gates, parity, govulncheck, e2e; commit `feat(services): …`.
