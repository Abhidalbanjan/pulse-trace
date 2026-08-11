# SLOs — Enhancement Spec

**Route:** `/slo` · **Component:** `frontend/src/components/SLO/SLOView.tsx` · **Backend:** correlation-service SLO handler/worker (`/api/v1/slo/definitions|dashboard|budget-alerts`)

## 1. Where it stands

- SLO definitions CRUD, a dashboard, budget alerts, and burn-rate (F2).
- SLIs computed from Quickwit (fallback Postgres).
- Consumed by the Catalog scorecard column (F13).

## 2. Market-ready gap

The core is there, but the parts that make SLOs *operational* — Google-SRE **multi-window multi-burn-rate** alerting, budget-driven **deploy freezes**, per-service **templates**, and **stakeholder reporting** — are missing. Without them SLOs are a dashboard, not a policy.

## 3. Proposed enhancements

### E1. Multi-window, multi-burn-rate alerting · **M**
- **User value:** page fast on a hard breach, ticket slowly on a slow burn — the industry-standard low-noise SLO alerting.
- **What:** the 4-window burn-rate policy (e.g. 2%/1h + 5%/6h fast page; 10%/3d slow ticket) per SLO.
- **Backend:** burn-rate evaluation over multiple windows in the SLO worker; emit through the alert pipeline.
- **Frontend:** per-SLO alert-policy config with sensible defaults.

### E2. Budget-driven deploy freeze (ties to Deploy Gates) · **M**
- **User value:** when the error budget is exhausted, risky deploys are automatically blocked — reliability enforced, not hoped for.
- **What:** an error-budget policy that, when burned past a threshold, flips the F5 deploy gate to "blocked" for that service.
- **Backend:** SLO budget state → deploy-gate decision; `GET /api/v1/slo/{id}/budget-policy`.
- **Frontend:** policy toggle + a "deploys frozen" banner on the service.

### E3. SLO templates & bulk creation · **S**
- **User value:** define availability + latency SLOs for a new service in seconds.
- **What:** templates (availability 99.9%, latency p99 < 300ms) applied per service from the Catalog.
- **Backend:** template apply endpoint that instantiates definitions.
- **Frontend:** "Add SLO from template" in SLOView + Catalog.

### E4. Budget-burn forecasting · **S**
- **User value:** *"at this rate you'll exhaust the budget in 4 days"* — act before the breach.
- **What:** project current burn to a run-out date; show on the dashboard.
- **Backend:** linear/EWMA projection over recent burn.
- **Frontend:** forecast line + "budget runs out" callout.

### E5. Stakeholder SLO report · **S**
- **User value:** a monthly reliability report for leadership, no spreadsheet.
- **What:** per-period SLO attainment, budget consumed, top incidents; export to PDF/Markdown.
- **Backend:** report aggregation endpoint.
- **Frontend:** "Export report" with period picker.

### E6. Journey / composite SLOs · **M**
- **User value:** SLO on a user flow (checkout) spanning multiple services, not just one endpoint.
- **What:** composite SLI combining multiple service SLIs with weights/AND semantics.
- **Backend:** composite SLI definition + evaluation.
- **Frontend:** journey SLO builder.

## 4. Market-ready DoD

- Every SLO can drive low-noise multi-burn-rate alerts and (optionally) freeze deploys when its budget is spent.
- New services get SLOs from templates in seconds; budgets forecast their run-out.
- Leadership can export a reliability report.

## 5. Suggested sequence

E1 (burn-rate alerting) → E2 (deploy freeze) → E4 (forecast) → E3 (templates) → E5 (report) → E6 (journey).
