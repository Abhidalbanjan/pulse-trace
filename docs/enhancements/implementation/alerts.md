# Alerts — Implementation Plan

Spec: [../alerts.md](../alerts.md) · Services: **gateway** (alert-rule CRUD), **correlation** (`AlertRuleEvaluator`), **notification** · View: `frontend/src/components/Alerts/AlertsView.tsx`

## Current state (grounded)
- Alerts list (`/api/v1/alerts`, `/{id}`); user-defined alert rules (`/api/v1/admin/alert-rules`) evaluated by correlation `AlertRuleEvaluator`; anomaly config (F14); channels (F3).

## E1 — Grouping & dedup · M  *(recommended first slice)*
- Pure `groupKey(alert)` (service+rule+labels) + `groupAlerts(alerts) []AlertGroup{key, count, first, last, sample}`. `GET /api/v1/alerts?group=true`. FE grouped list, expand-to-instances. No migration (compute over existing alerts).
- Tests: grouping/dedup table-driven; e2e grouped view.

## E2 — Silences & maintenance windows · M
- **Data (gateway migration 025):** `alert_silences(id, tenant_id, matcher JSONB, starts_at, ends_at, created_by, recurring JSONB NULL)`. Pure `silenceMatches(silence, alert, now)`. Evaluator/notifier skip paging when a silence matches. CRUD `/api/v1/alerts/silences`. FE "Silence" action + manager.

## E3 — Composite / multi-condition rules · M
- Extend the alert-rule model with a condition tree + `for` duration; pure `evaluateConditionTree(tree, metricsSnapshot)`. Reuse the ABAC **guided-builder** UI pattern (`PoliciesPanel`). Evaluator honors `for`.

## E4 — Anomaly-based rule type · M
- Bridge `anomaly_config` + EWMA detector into the evaluator as a rule `type=anomaly`. FE anomaly rule type + sensitivity slider.

## E5 — Routing & escalation · M
- `alert_routes(matcher, channel/team)` + escalation chains; notification-service honors them. FE routing table.

## E6 — History, flap detection & test-fire · S
- Per-rule fire history + pure `flapScore(history)`; "Test notification" (reuse channel test); per-alert runbook URL field. FE rule detail.

## Sequencing & gates
E1 → E2 → E3 → E4 → E6 → E5. Per slice: touched module build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(alerts): …`.
