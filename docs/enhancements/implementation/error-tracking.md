# Error Tracking — Implementation Plan

Spec: [../error-tracking.md](../error-tracking.md) · Service: **gateway** (Postgres `error_groups`) · View: `frontend/src/components/Errors/ErrorTrackingView.tsx`

## Current state (grounded)
- Error groups + occurrence timeline (`/api/v1/errors/groups/{fingerprint}/timeline`), regression worker (new/regression, auto-reopen) (F11). `error_groups` has `tenant_id`.

## E1 — Triage workflow · M  *(recommended first slice)*
- **Data (gateway migration 025):** add `status` (unresolved|resolved|ignored|snoozed), `assignee`, `snoozed_until` to `error_groups`. Pure `effectiveStatus(group, now)` (snooze expiry → unresolved). `PATCH /api/v1/errors/groups/{fingerprint}` (status/assignee/snooze). List filters. Auto-reopen on regression already exists — respect it.
- FE status controls + assignee + filters. Parity: route consumed.
- Tests: `effectiveStatus` (snooze expiry, resolved→regression reopen); e2e status change.

## E2 — Release health · M
- Capture release/version on error events; per-release aggregation `GET /api/v1/errors/releases`; new/regressed/resolved-in-release + crash-free rate. FE release panel.

## E3 — Stack-trace source context · M
- Store/parse frames (in-app vs library); expose on group detail; explain the grouping fingerprint. FE stack viewer.

## E4 — Impact scoring · S
- Join errors → RUM sessions/users/tenants; pure `impactScore`. FE "users affected" column + sort.

## E5 — Issue creation & Slack · S
- Create issue/Slack thread from a group via notification-service channels. FE "Create issue".

## E6 — Similar-errors clustering · S
- Pure similarity over fingerprints/messages; FE "similar" section.

## Sequencing & gates
E1 → E2 → E3 → E4 → E5 → E6. Per slice: gateway build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(errors): …`.
