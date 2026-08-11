# Metrics — Implementation Plan

Spec: [../metrics.md](../metrics.md) · Service: **gateway-service** (ClickHouse `otel_metrics_*`) · View: `frontend/src/components/Metrics/MetricsView.tsx`

## Current state (grounded)

- `metrics_handler.go`: `/api/v1/metrics`, `/api/v1/metrics/query` with `fn=avg|min|max|sum|rate|p50|p90|p95|p99` via pure `metricAggExpr`; unit-aware FE charts (F8). ClickHouse via `queryScoped`.

## E1 — Metric explorer / browser · M  *(recommended first slice)*
- `GET /api/v1/metrics/catalog` → distinct metric names + label keys/values (from `otel_metrics_gauge`/`_sum`), tenant-scoped. Pure `parseLabelFacets(rows)`.
- FE: explorer sidebar (search names, show labels) → clicking graphs the metric. Parity: route consumed.
- Tests: catalog SQL builder + facet parse; e2e explorer lists metrics.

## E2 — Saveable dashboards · L
- **Data (gateway migration 025):** `dashboards(id, tenant_id, name, layout JSONB, created_by, updated_at)` (+ panels embedded in layout JSON).
- **Backend:** `dashboard_handler.go` CRUD `GET/POST/PUT/DELETE /api/v1/dashboards[/{id}]`, tenant-scoped, RBAC for write. Pure `validateDashboard(spec)`.
- **FE:** dashboard grid (add/edit/drag panels, each a metric query), shareable URL. Parity: routes consumed.
- Tests: CRUD validation; e2e create+save+reload.

## E3 — Template variables · M
- `$service`/`$env` resolved from the catalog (E1); applied across panels client-side; values via `GET /api/v1/metrics/catalog?label=service`. FE variable bar.

## E4 — Multi-series & math · M
- Extend `/metrics/query` to accept `series[]` + `expr` (e.g. `a/b`). Pure `evalMetricExpr(seriesValues, expr)` (safe mini-evaluator, allowlisted ops) — unit-test heavily (injection/΅div-by-zero). FE multi-query panel editor.

## E5 — Alert from query · S
- "Create alert" from a panel → prefilled alert rule (reuse alert-rule model). FE affordance; backend already has alert-rule CRUD.

## E6 — Anomaly & change overlay · S
- Overlay F14 anomaly bands + shared `<DeployMarkers>` (Deploy-Gates E1) on panels. FE-only once markers/anomaly endpoints exist.

## Sequencing & gates
E1 → E2 → E3 → E4 → E5 → E6. Per slice: gateway build/vet/test (ratchet), FE gates, parity, govulncheck, e2e; commit `feat(metrics): …`.
