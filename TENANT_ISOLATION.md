# PulseTrace — Tenant Isolation Model

_ROAD_TO_100 · F0.3 — the R5 (security & tenancy) evidence. How a tenant's data is
kept separate across every store, and how the codebase is prevented from
regressing that guarantee._

## Threat model

A tenant must never read or write another tenant's telemetry. The two failure
modes we defend against:

1. **Forged identity** — a caller claims to be another tenant (a spoofed
   `X-Tenant-ID` header, a client-supplied `tenant_id` in a body).
2. **Forgotten scope** — a query against a per-tenant store omits its tenant
   filter and silently returns everyone's rows.

## Identity is resolved server-side, never from the client

Tenant identity is established by the gateway and is **not** taken from any
client-supplied header or body field:

- **Authenticated requests:** `AuthMiddleware` sets `X-Tenant-ID` on the request
  from the verified JWT (or the resolved ingestion key). Handlers read the tenant
  only via `tenantFromRequest(r)` / the stamped header — which by that point is
  gateway-controlled, not attacker-controlled.
- **Ingestion:** the tenant comes from the server-side ingestion key
  (`ingestion_keys`, SHA-256 hashed), resolved in `ingestproxy.resolveTenant`. A
  public `rum`-scoped token can attribute browser RUM but is rejected on
  server-telemetry paths.

The cross-tenant e2e (`scripts/test-tenant-isolation.mjs`) proves this directly:
tenant A's user forging `X-Tenant-ID: <tenant B>` on a read still sees only A's
data — the forged header is inert.

## Where tenant data lives, and how each store is scoped

| Store | Tenant-scoped data | How it's isolated |
| --- | --- | --- |
| **Postgres** | incidents, error triage state, synthetic targets, ingestion keys, users, policies | `WHERE tenant_id = $n` per query; unique constraints include `tenant_id`. |
| **ClickHouse** (collector-owned) | `otel_traces`, `otel_logs`, `otel_metrics_*` | `ResourceAttributes['tenant.id'] = {tenant:String}` — the tenant is stamped as an OTLP resource attribute by the gateway's in-process receiver and persisted by the collector. |
| **ClickHouse** (app-owned) | `rum_events`, `synthetic_results` | explicit `TenantID` column, **`PARTITION BY TenantID`** (so isolation is physical and per-tenant deletion is a cheap partition drop). |
| **Quickwit** | `pulsetrace-logs` index | `tenant_id` field filter on every search. |
| **Neo4j** | topology graph | tenant property on nodes/edges; queries filter by it. |

## Audit findings (fixed)

The F0.3 read-path audit found and fixed two **live cross-tenant read leaks** in
services outside the gateway (both authenticated, gateway-reachable routes):

| Endpoint | Leak | Fix |
| --- | --- | --- |
| `GET /api/v1/incidents/{id}` and `/{id}/timeline` (correlation-service) | Read incidents by ID with no tenant filter — another tenant's incident (title, root cause, causal analysis, timeline) was readable by ID. | Use `GetByIDForTenant`; return not-found on a tenant mismatch. |
| `GET /api/v1/alerts` and `/api/v1/alerts/{id}` (alert-service) | The list never set `TenantID`, so the repository's tenant filter was skipped and it returned **every** tenant's alerts; the by-ID read was unscoped. | List always sets `TenantID`; by-ID uses a new `GetByIDForTenant`. |

Topology (Neo4j) reads already pass the caller's tenant on every query; log-service
by-ID reads already filter Quickwit by `tenant_id`. No other leaks were found.

## Enforcement: forgetting the filter is a build failure, not a leak

The ClickHouse read path is raw SQL over an HTTP client, so "remember to add the
tenant clause" was a convention a new handler could silently break. F0.3 turns it
into an enforced invariant with two layers, both in
[`gateway-service/internal/handler`](gateway-service/internal/handler/):

1. **Runtime guard** — `clickHouseClient.queryScoped(tenantID, sql, params)` is the
   only sanctioned way to read tenant data. It (a) refuses an empty tenant, (b)
   injects the `tenant` bind param from one trusted source, and (c) **fails closed**
   if the SQL reads a tenant-scoped table without a tenant predicate. All eleven CH
   read sites were migrated onto it.
2. **Static ratchet (gateway)** — `TestNoRawTenantTableReads` scans the handler
   package and fails the build if any raw `.query()` call reads a tenant-scoped
   table, preventing a future handler from bypassing the runtime guard. (Proven to
   catch a planted violation.)

Unit tests (`clickhouse_tenant_test.go`) cover the guard: unscoped reads and empty
tenants are rejected; scoped reads and non-tenant/system queries pass.

For the Postgres services, the same ratchet idea guards the by-ID read pattern that
caused the leaks above: `TestHandlersUseTenantScopedIncidentReads`
(correlation-service) and `TestHandlersUseTenantScopedAlertReads` (alert-service)
fail the build if any request handler calls the unscoped `GetByID` instead of
`GetByIDForTenant`. The unscoped method stays available for internal callers that
have already resolved the tenant (e.g. the correlation engine), which live outside
`internal/handler`.

## Known limitation (honest scope)

The plan's "add `tenant_id` to ClickHouse partition/order keys" is **only partly
applicable**: `otel_traces`/`otel_logs`/`otel_metrics_*` are created and owned by
the OTel Collector's ClickHouse exporter, which fixes their `ORDER BY`/partition
scheme (tenant lives in the `ResourceAttributes` map, not a top-level key). We
cannot change those keys from our migrations without recreating tables the
collector would re-provision its own way. The app-owned tables that we *do* control
(`rum_events`, `synthetic_results`) are already `PARTITION BY TenantID`.

**Forward path** (deferred, tracked for F19 cheap-deletion + isolation locality):
introduce a PulseTrace-owned, tenant-keyed materialized view fed from the
collector tables (`ORDER BY (tenant_id, ...)`, `PARTITION BY tenant_id`), and read
from that instead of the raw collector tables — giving physical tenant locality and
partition-drop deletion for traces/logs/metrics too.

## Verifying

```bash
# unit: the guard + the static ratchet (no stack needed)
cd gateway-service && GOWORK=off go test ./internal/handler/ -run 'Tenant|Scoped'

# e2e: full isolation chain + header-spoof resistance (needs the running stack)
node scripts/test-tenant-isolation.mjs
```

The e2e runs in CI (`ci.yml` → `e2e` → "Cross-tenant isolation test").
