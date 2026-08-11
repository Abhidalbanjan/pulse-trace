# SLOs — Implementation Plan

Spec: [../slos.md](../slos.md) · Service: **correlation-service** · Views: `frontend/src/components/SLO/SLOView.tsx`, Catalog

## Current state (grounded)

- `SLODefinition{ServiceName, SLOTarget, WindowDays}`, `SLOSnapshot`, `SLOBudgetAlert`, `SLODashboardItem{BudgetTotalMin, BudgetUsedMin, BudgetRemainingPct, BurnRate, Trend}` in `shared/models/slo.go`.
- `SLOWorker` (60s tick) computes SLI via `repo.ComputeSLI`, stores snapshots, calls `BurnRateAlerter.Evaluate`.
- `BurnRateAlerter` + pure `computeBurnRate(currentSLI, sloTarget, windowDays)` + `DefaultBurnRateThresholds` (14×/1h CRIT, 6×/6h WARN, 1×/72h INFO) — single-window today.
- Routes: `GET/POST /slo/definitions`, `DELETE /slo/definitions/{id}`, `GET /slo/dashboard`, `GET /slo/budget-alerts`, `POST /slo/evaluate-pr`.

## E1 — Multi-window, multi-burn-rate alerting · M

- **Gap:** `Evaluate` uses one window (`WindowDays`). True MWMBR requires each threshold's **short + long** windows to *both* breach (Google SRE): page only when the burn is severe *and* recent.
- **Data:** none new. Compute SLI over each threshold window from snapshots/`ComputeSLI` at (now−window).
- **Backend:**
  - Extend `BurnRateThreshold` → `{LongWindow, ShortWindow, Multiplier, Severity}`; default table = the 4-window SRE policy (2%/1h+5m, 5%/6h+30m, 10%/3d+6h…).
  - New pure `evaluateMultiWindow(sliByWindow map[time.Duration]float64, target float64, thresholds []BurnRateThreshold) (fired *BurnRateThreshold)` — burn over long AND short window ≥ multiplier. Unit-test table-driven (extend `burn_rate_alerter_test.go`).
  - `BurnRateAlerter.Evaluate` computes SLI per distinct window (batch via repo) then calls the pure fn. Keep single-window path as a fallback when only one snapshot window exists.
- **Frontend:** SLOView per-SLO "alert policy" readout (which windows/severities). Optional editable later.
- **Tests:** `evaluateMultiWindow` (both breach → fire; only long → no page; only short → no page; severity ordering). **Verify:** correlation build/vet/test.

## E2 — Budget-driven deploy freeze · M

- **Gap:** SLO budget state doesn't influence the F5 deploy gate.
- **Data:** derive from live budget; optional `slo_budget_policies(service_name, freeze_below_pct, enabled)` (migration **006**).
- **Backend:**
  - Pure `shouldFreezeDeploys(budgetRemainingPct, freezeBelowPct float64, enabled bool) bool`.
  - Wire into `EvaluatePR` (`/slo/evaluate-pr`): if the service's budget < policy threshold, the PR gate returns **blocked** with a reason. Reuse existing evaluate-pr response shape.
  - `GET /api/v1/slo/{service}/budget-policy`, `PUT` to set it (admin-gated).
- **Frontend:** SLOView policy toggle + "deploys frozen" banner; Deploy-Gates view shows the freeze reason.
- **Parity:** new routes → consumed by SLOView.
- **Tests:** `shouldFreezeDeploys` truth table; EvaluatePR-blocks-when-exhausted (repo faked).

## E4 — Budget-burn forecasting · S  *(recommended first slice — self-contained)*

- **Gap:** no run-out projection.
- **Data:** none — projects from recent snapshots.
- **Backend:**
  - Pure `forecastBudgetExhaustion(points []SLOTrendPoint, budgetRemainingPct float64, now time.Time) (exhaustAt *time.Time, daysLeft float64, burning bool)` — linear/EWMA slope of budget-remaining over recent snapshots; if slope < 0, project to 0. Returns `burning=false` when improving/flat.
  - Add `ForecastExhaustAt *time.Time` + `ForecastDaysLeft float64` to `SLODashboardItem`; populate in `Dashboard` handler from the trend it already builds.
- **Frontend:** SLOView — a forecast line/badge: *"Budget runs out in ~4d (Aug 15)"* when `burning`, else "on track".
- **Tests:** `forecastBudgetExhaustion` (declining → date; improving → not burning; flat → not burning; already-zero → now). **Verify:** correlation build/test + FE tsc/build.

## E3 — Templates · S
- Client-side templates (availability 99.9 / latency p99) POSTing existing `CreateSLORequest`; add "Add SLO from template" to SLOView + Catalog. No backend change beyond bulk-create convenience.

## E5 — Stakeholder report · S
- `GET /api/v1/slo/report?from=&to=` aggregating attainment + budget consumed + top incidents; FE "Export report" (client-side Markdown/print). Register route → consumed.

## E6 — Journey/composite SLOs · M
- New `slo_composites(name, member_service_ids[], semantics)` (migration); composite SLI = weighted/AND of member SLIs; extend Dashboard. Larger — do last.

## Sequencing & gates

E4 (forecast) → E1 (MWMBR) → E2 (deploy freeze) → E3 (templates) → E5 (report) → E6 (journey).
Each slice: correlation `GOWORK=off go build/vet/test`, FE `tsc/lint/build`, parity 100%, govulncheck, e2e for the dashboard/forecast render; commit `feat(slo): …`.
