# Distributed Traces — Implementation Plan

Spec: [../traces.md](../traces.md) · Service: **gateway-service** (ClickHouse `otel_traces`) · View: `frontend/src/components/Traces/`

## Current state (grounded)

- `TraceWaterfall.tsx` renders a waterfall and pivots to Explorer; it reaches data via the logs search path. No first-class trace search endpoint over `otel_traces`.
- ClickHouse access pattern: `clickHouseClient.queryScoped(tenant, sql, params)` with `tenantClause = ResourceAttributes['tenant.id'] = {tenant:String}`; helpers `stringParam`, `intervalToSQL`. Ratchet test `TestNoRawTenantTableReads`.

## E1 — First-class trace search & explorer · L  *(recommended first slice, whole program)*

- **Data:** none new — query `otel_traces` (cols: `TraceId, SpanId, ParentSpanId, ServiceName, SpanName, Timestamp, Duration, StatusCode, SpanAttributes, ResourceAttributes`).
- **Backend (`gateway-service/internal/handler/traces_handler.go`, new):**
  - `GET /api/v1/traces` — params: `service, operation, minDurationMs, maxDurationMs, status(error|ok|any), tag=k:v (repeatable), from, to, limit(≤200)`. Returns trace summaries: `{trace_id, root_service, root_operation, start, duration_ms, span_count, error_count, status}` — group by `TraceId`, root = min-timestamp/null-parent span.
  - `GET /api/v1/traces/{id}` — all spans for a trace id (for the waterfall + span detail), tenant-scoped.
  - Pure helpers to unit-test: `buildTraceSearchSQL(filters) (sql, params)` (allowlist columns/operators, param-bind values — never string-concat user input), `parseTagFilters([]string) []kv`, `classifyTraceStatus(spans)`.
  - All queries via `queryScoped`; add both routes.
- **Frontend:** rebuild `/traces` as **search + results table + waterfall**: filter bar (service/op/duration/status/tag), results list, existing waterfall on row-select fed by `/traces/{id}`. Keep the F7 "Open in Explorer" pivot.
- **Parity:** `GET /api/v1/traces` + `/api/v1/traces/{id}` consumed by the view.
- **Tests:** `buildTraceSearchSQL` (filter combos → param-bound SQL, injection-safe), `parseTagFilters`, `classifyTraceStatus`; e2e: search returns rows and a row opens a waterfall.
- **Verify:** gateway build/vet/test (incl. `TestNoRawTenantTableReads`), FE tsc/lint/build, parity, govulncheck.

## E2 — Latency distribution & percentile heatmap · M
- `GET /api/v1/traces/latency?service=&operation=&from=&to=&buckets=` → duration histogram + p50/p95/p99 via ClickHouse `quantile`/`histogram`. Pure `bucketConfig`. FE distribution chart with click-bucket → `/api/v1/traces?minDurationMs=…`.

## E3 — Span detail & events panel · S
- Extend `/traces/{id}` to include `SpanAttributes`, span events, error info. FE side-panel on span click.

## E4 — Service map from traces · M
- `GET /api/v1/traces/service-map?from=&to=` → aggregate parent→child `ServiceName` edges with count/error/p95. Pure edge-rollup. Feeds a map (shared with Topology E1).

## E5 — Critical path & flame · M
- Compute critical path from span tree (pure `criticalPath(spans)`); aggregate flame per operation. FE highlight + flame.

## E6 — Metric→trace exemplars · S
- Associate trace ids to metric time buckets; "view traces" from metric points. Depends on Metrics dashboards.

## Sequencing & gates
E1 → E3 → E2 → E4 → E5 → E6. Per slice: gateway build/vet/test (ratchet), FE gates, parity, govulncheck, e2e; commit `feat(traces): …`.
