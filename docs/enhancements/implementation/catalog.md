# Catalog — Implementation Plan

Spec: [../catalog.md](../catalog.md) · Service: **topology-service** (catalog) + **gateway** · View: `frontend/src/components/Catalog/ServiceCatalog.tsx`

## Current state (grounded)
- Catalog with team/repo/Slack, search, SLO-budget scorecard column (F13). Agents register services (`/api/v1/topology/catalog`).

## E3 — Rich service metadata & lifecycle · S  *(recommended first slice)*
- Extend the catalog record with `tier`, `lifecycle` (experimental|production|deprecated), and structured `links` (repo/dashboards/runbooks/docs). `PATCH /api/v1/topology/catalog/{service}`. FE metadata panel + lifecycle badges. Parity: route consumed.
- Tests: metadata validation; e2e edit metadata.

## E1 — Production-readiness scorecards · L
- **Data:** `scorecard_rules(id, tenant_id, name, predicate JSONB, weight)`. Pure `evaluateScorecard(service, rules) (grade A–F, checks[])` over catalog/SLO/incident data. `GET /api/v1/catalog/scorecards`. FE grade badges + scorecard detail + a rules editor (mirror ABAC guided builder).
- Tests: `evaluateScorecard` grading; e2e grades render.

## E2 — Ownership & on-call · M
- On-call sync adapter (PagerDuty/Opsgenie or F18 teams); store current on-call. FE owner + live on-call column.

## E4 — Dependencies & tiers · S
- Surface upstream/downstream + tier from Topology on the entry; FE dependency chips.

## E6 — Team view & rollup · S
- Group catalog by team with rolled-up score/health; FE team grouping.

## E5 — Bulk import / auto-discovery · M
- Import from k8s labels / repo `catalog.yaml`; import endpoint + reconciler; FE "Import services".

## Sequencing & gates
E3 → E1 → E2 → E4 → E6 → E5. Per slice: touched module build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(catalog): …`.
