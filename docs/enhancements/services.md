# Services — Enhancement Spec

**Route:** `/services` · **Components:** `frontend/src/components/Services/{ServicesView,ServiceDetailView,DeploymentsPanel}.tsx` · **Backend:** gateway `/api/v1/services`, `/api/v1/deployments`, `/api/v1/profiler/…`

## 1. Where it stands

- A services list and a service detail view that stitches together deployments, profiler, and basic service info.

## 2. Market-ready gap

Service detail is a hub of links, not a **golden-signals command center**. The market bar (Datadog APM service page, New Relic entity view) is a single page that answers "is this service healthy, and if not, why" — RED metrics, dependencies, SLOs, recent deploys/incidents, and ownership — all in one glance.

## 3. Proposed enhancements

### E1. RED golden-signals dashboard per service · **M**
- **User value:** rate, errors, duration (p50/p95/p99) for the service at a glance.
- **What:** the canonical service header — request rate, error rate, latency percentiles — from traces/metrics, with deploy markers overlaid.
- **Backend:** `GET /api/v1/services/{name}/signals?range=` (ClickHouse aggregation).
- **Frontend:** RED chart row at the top of the detail view.

### E2. Service health score · **S**
- **User value:** one number/color that ranks services by health for triage.
- **What:** a composite score from error rate + latency vs SLO + open incidents; shown on the list and detail.
- **Backend:** score computed from signals + SLO + incidents.
- **Frontend:** health badge + sortable list column.

### E3. Dependencies in/out with health · **M**
- **User value:** see upstreams/downstreams and whether a dependency is dragging this service down.
- **What:** in/out dependency lists (from Topology/traces) with per-dependency health + latency contribution.
- **Backend:** reuse topology upstream/downstream + trace edge latency.
- **Frontend:** dependency panel with health chips → click to that service.

### E4. Unified activity timeline · **S**
- **User value:** deploys, incidents, and alerts for this service on one timeline — instant "what changed."
- **What:** merge deployments + incidents + alerts into a single per-service timeline.
- **Backend:** time-ranged join across the three.
- **Frontend:** timeline strip on the detail view.

### E5. SLO & error-budget status · **S**
- **User value:** the service's SLOs and remaining budget, inline.
- **What:** surface the service's SLO attainment + budget from the SLO subsystem.
- **Frontend:** SLO tiles on the detail view (reuse SLO components).

### E6. Ownership & operational metadata · **S**
- **User value:** who owns it, on-call, repo, runbook, dashboards — from the Catalog.
- **What:** pull the Catalog entry (team, Slack, repo, on-call) onto the service page.
- **Frontend:** ownership card linking to Catalog.

## 4. Market-ready DoD

- The service page opens with RED signals + a health score and answers "healthy? why?" without navigating away.
- Dependencies, SLO/budget, a change timeline, and ownership are all on one page.

## 5. Suggested sequence

E1 (RED dashboard) → E2 (health score) → E4 (activity timeline) → E5 (SLO tiles) → E3 (dependencies) → E6 (ownership).
