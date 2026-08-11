# Feature Enhancement — Implementation Plans

The engineering **"how"** for each spec in [`../`](../). A spec says *what* to build
and *why*; the matching plan here says *exactly how* — data model + migration,
endpoint signatures, backend/frontend files, the pure logic to unit-test, the
tests to add, the parity-registry treatment, and the verification gates per
slice. Each plan is executable top-to-bottom without further design.

| Spec | Implementation plan |
| --- | --- |
| [ai-sre](../ai-sre.md) | [ai-sre.md](ai-sre.md) |
| [incidents](../incidents.md) | [incidents.md](incidents.md) |
| [alerts](../alerts.md) | [alerts.md](alerts.md) |
| [slos](../slos.md) | [slos.md](slos.md) |
| [deploy-gates](../deploy-gates.md) | [deploy-gates.md](deploy-gates.md) |
| [onboarding](../onboarding.md) | [onboarding.md](onboarding.md) |
| [log-explorer](../log-explorer.md) | [log-explorer.md](log-explorer.md) |
| [traces](../traces.md) | [traces.md](traces.md) |
| [services](../services.md) | [services.md](services.md) |
| [metrics](../metrics.md) | [metrics.md](metrics.md) |
| [error-tracking](../error-tracking.md) | [error-tracking.md](error-tracking.md) |
| [profiler](../profiler.md) | [profiler.md](profiler.md) |
| [rum](../rum.md) | [rum.md](rum.md) |
| [synthetics](../synthetics.md) | [synthetics.md](synthetics.md) |
| [topology](../topology.md) | [topology.md](topology.md) |
| [catalog](../catalog.md) | [catalog.md](catalog.md) |
| [settings](../settings.md) | [settings.md](settings.md) |

## Codebase conventions every plan follows

**Backend (Go)**
- **gateway-service** — HTTP handlers in `internal/handler/`, registered in `cmd/main.go`; ClickHouse via `clickHouseClient.queryScoped(tenant, sql, params)` (tenant-scoping enforced + ratcheted by `TestNoRawTenantTableReads`); Postgres migrations `gateway-service/migrations/NNN_*.sql` (next = **025**), embedded + applied by `shared/migrate` at boot.
- **correlation-service** — handlers with `RegisterRoutes(mux)`, repositories in `internal/repository/`, background workers in `internal/engine/` (start in `cmd/main.go`); migrations `correlation-service/migrations/NNN_*.sql` (next = **006**).
- Tenant identity is resolved server-side from JWT/ingest key, never a client header. Admin surfaces gated by the RBAC middleware.
- Extract decision/algorithm logic into **pure functions** and unit-test them (table-driven), matching e.g. `computeBurnRate`, `evaluateAssertion`, `GroundAnalysis`.

**Frontend (Next.js / React 19)**
- Views in `src/components/<Feature>/`; typed `api` client (`api.getData`) or `fetchWithAuth`; `useTheme()` tokens `t`. No synchronous `setState` in effects (debounce/guard).
- New backend route → a real UI consumer **or** register it in `scripts/parity/registry.json` `uiNone`. Keep the **parity gate at 100%**.

**Per-slice gates (must be green before commit)**
- Affected Go module(s): `GOWORK=off go build ./... && go vet ./... && go test ./...`.
- Frontend: `npx tsc --noEmit && npm run lint && npm run build`.
- `node scripts/parity/check-parity.mjs` · `govulncheck ./...` on touched Go modules.
- Add e2e in `frontend/tests/e2e/` for the happy path; commit the slice with a `feat(<area>): … ` message.

## Global sequencing recommendation

Implement **one enhancement (E#) per slice**, in each spec's own suggested order.
Cross-feature, the highest demo/market leverage first:

1. Traces **E1** (trace search) — biggest depth gap.
2. Incidents **E1** (AI postmortem) — flagship AI showcase.
3. Metrics **E2** (dashboards) — unlocks Deploy-markers/anomaly overlays reuse.
4. SLOs **E1/E2** (burn-rate alerting + deploy freeze) — reliability story.
5. Then breadth across the remaining pillars per each plan.
