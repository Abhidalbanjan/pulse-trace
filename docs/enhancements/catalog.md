# Catalog — Enhancement Spec

**Route:** `/catalog` · **Component:** `frontend/src/components/Catalog/ServiceCatalog.tsx` · **Backend:** topology-service catalog (`/api/v1/topology/catalog`, `/graph`), SLO dashboard

## 1. Where it stands

- A service catalog with team/repo/Slack metadata, **search**, and an **SLO-budget scorecard column** (F13). Agents can register services into the catalog.

## 2. Market-ready gap

It's a good inventory. The market bar (Backstage, Cortex, OpsLevel) is a **production-readiness scorecard** engine: rules that score each service on ownership, SLOs, on-call, runbooks, and reliability — turning the catalog into a governance tool leadership uses to drive maturity.

## 3. Proposed enhancements

### E1. Production-readiness scorecards · **L**
- **User value:** every service gets a maturity grade (A–F) against rules: has owner? has SLO? on-call set? runbook linked? error budget healthy? recent incidents?
- **What:** a rule engine scoring each service; per-service scorecard + org rollup.
- **Backend:** `scorecard_rules` + evaluation over catalog/SLO/incident data; `GET /api/v1/catalog/scorecards`.
- **Frontend:** grade badges, a scorecard detail, and a rules editor (mirror the ABAC guided builder).

### E2. Ownership & on-call from a source of truth · **M**
- **User value:** who owns it and who's on call *right now* — synced, not stale.
- **What:** pull on-call from PagerDuty/Opsgenie (or the F18 users/teams); show current on-call per service.
- **Backend:** on-call sync adapter; store current on-call.
- **Frontend:** owner + live on-call column.

### E3. Rich service metadata & links · **S**
- **User value:** one place for repo, dashboards, runbooks, docs, tier, lifecycle.
- **What:** structured metadata (tier, lifecycle experimental/prod/deprecated, links) on each entry.
- **Backend:** extend the catalog record.
- **Frontend:** metadata panel + lifecycle badges.

### E4. Dependencies & tiers view · **S**
- **User value:** see a service's tier and its critical dependencies at a glance.
- **What:** surface upstream/downstream + tier from Topology on the catalog entry.
- **Frontend:** dependency chips linking to Topology/Services.

### E5. Bulk import / auto-discovery · **M**
- **User value:** populate the catalog from k8s/git automatically instead of by hand.
- **What:** import services from k8s labels / a `catalog.yaml` in each repo (Backstage-style).
- **Backend:** import endpoint + reconciler.
- **Frontend:** "Import services" flow.

### E6. Team view & ownership rollup · **S**
- **User value:** a per-team page — their services, scorecard average, open incidents.
- **What:** group the catalog by team with rolled-up health/score.
- **Frontend:** team grouping + rollup tiles.

## 4. Market-ready DoD

- Every service has a production-readiness grade from an editable rule engine, with live ownership/on-call and rich metadata.
- Services can be bulk-imported/auto-discovered; a team view rolls up scores and health.

## 5. Suggested sequence

E3 (metadata) → E1 (scorecards) → E2 (on-call) → E4 (dependencies) → E6 (team view) → E5 (bulk import).
