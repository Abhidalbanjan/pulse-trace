# Log Explorer — Implementation Plan

Spec: [../log-explorer.md](../log-explorer.md) · Service: **gateway** (Quickwit + `otel_logs`) · View: `frontend/src/components/Explorer/ExplorerView.tsx`

## Current state (grounded)
- Quickwit search (`/api/v1/search/pulsetrace-logs/search`), saved searches (`/api/v1/saved-searches`), context view (`/api/v1/logs/{id}/context`), shareable query URLs (F6), log→trace pivot (F7).

## E2 — Facet / field sidebar · M  *(recommended first slice)*
- `GET /api/v1/logs/facets?q=&fields=service,level,host` → Quickwit term aggregations (top values + counts). Pure `buildFacetAgg(fields)`. FE left rail of fields → click adds to query. Parity: route consumed.
- Tests: agg builder; e2e facets render + click filters.

## E5 — Histogram with brush-to-zoom · S
- `GET /api/v1/logs/histogram?q=&from=&to=&buckets=` → Quickwit date-histogram. FE histogram above results; brush sets range.

## E1 — Pattern clustering · L
- Pure `extractPattern(message)` (Drain-style tokenization: mask numbers/uuids/paths) + `clusterLogs(messages) []Pattern{template, count, trend}`. `GET /api/v1/logs/patterns?q=`. FE "Patterns" tab. Unit-test tokenization heavily.

## E3 — Live tail · M
- SSE/poll tail honoring the current query + tenant; FE "Live" toggle + auto-scroll.

## E4 — Log-to-metric & alert-from-search · M
- Save query → generated metric (count/rate) evaluated on a schedule → metrics store; hook alert-rule model. FE "Create metric"/"Alert on this" from a saved search.

## E6 — Export · S
- Streaming NDJSON/CSV export endpoint (capped); FE "Export".

## Sequencing & gates
E2 → E5 → E1 → E3 → E4 → E6. Per slice: gateway build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(explorer): …`.
