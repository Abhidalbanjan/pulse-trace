# Deploy Gates — Implementation Plan

Spec: [../deploy-gates.md](../deploy-gates.md) · Service: **gateway** · View: `frontend/src/components/Deployments/DeploymentsView.tsx`

## Current state (grounded)
- F5: GitHub webhook (`/api/v1/webhooks/github`, HMAC), deploy feed + persistence (`/api/v1/deployments/gates`), PR shift-left (`/api/v1/slo/evaluate-pr`). `deployments` table (service, version, git_sha, environment, deployed_by).

## E1 — Deploy markers on every chart · M  *(recommended first slice)*
- `GET /api/v1/deployments?service=&from=&to=` → time-ranged deploys. Shared FE `<DeployMarkers>` overlay (vertical markers → deploy detail) reused by Metrics/Services/SLO/Errors charts. Parity: route consumed.
- Tests: time-range query; e2e markers render on a chart.

## E2 — DORA metrics · M
- `GET /api/v1/deployments/dora?from=&to=` → deploy frequency, lead time, change-failure rate, MTTR (join deployments + incidents). Pure `computeDORA(deploys, incidents)`. FE DORA tiles + trend.

## E3 — Post-deploy verification & auto-rollback · L
- correlation-service watcher: after a deploy, sample error-rate/latency/SLO burn for N min; pure `regressionVerdict(before, after, thresholds)`; on regression mark deploy failed + (policy-gated) rollback playbook via action-service. FE "verifying…" → pass/fail + rollback approval card.

## E4 — Change-failure linking · S
- Pure `linkIncidentToDeploy(incidentStart, deploys)` (nearest preceding same-service deploy). Add "caused by deploy" to incident payload; failure-rate on deploys.

## E5 — Multi-SCM / generic webhooks · M
- `/api/v1/webhooks/{provider}` adapters (GitLab/Bitbucket/generic) with per-provider signature verification (mirror github HMAC). Parity: uiNone (webhooks).

## E6 — Gate policy editor · S
- `deploy_gate_policies(service, rules JSONB)` consumed by `/slo/evaluate-pr`; FE policy editor.

## Sequencing & gates
E1 → E4 → E2 → E6 → E3 → E5. Per slice: gateway (+correlation for E3) build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(deploy): …`.
