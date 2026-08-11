# Deploy Gates — Enhancement Spec

**Route:** `/deployments` · **Component:** `frontend/src/components/Deployments/DeploymentsView.tsx` · **Backend:** gateway deploy-gates + GitHub webhook (`/api/v1/deployments/gates`, `/api/v1/webhooks/github`, `/api/v1/slo/evaluate-pr`)

## 1. Where it stands

- Shift-left deploy gates (F5): a real GitHub webhook, deployment feed, persistence, HMAC-verified, and a PR shift-left evaluation endpoint.

## 2. Market-ready gap

It gates on GitHub and evaluates a PR, but the loop isn't closed: no **DORA metrics**, no **auto-rollback** on a bad deploy, no **deploy markers** overlaid on the telemetry (the single most-used correlation feature in Datadog), and one SCM only. These are what make deploy gates a change-intelligence product.

## 3. Proposed enhancements

### E1. Deploy markers on every chart · **M**
- **User value:** *"latency jumped right after deploy v1.4.2"* — the fastest RCA there is.
- **What:** vertical deploy markers on Metrics/Services/SLO/Errors charts, clickable to the deploy detail + diff.
- **Backend:** deployments already stored; expose a time-ranged `GET /api/v1/deployments?service=&from=&to=`.
- **Frontend:** a shared `<DeployMarkers>` overlay used across chart views.

### E2. DORA metrics dashboard · **M**
- **User value:** the four keys leadership asks for — deploy frequency, lead time, change-failure rate, MTTR.
- **What:** compute DORA from deployments + incidents; trend + per-service breakdown.
- **Backend:** aggregation `GET /api/v1/deployments/dora`.
- **Frontend:** DORA header with the four tiles + trends.

### E3. Post-deploy verification & auto-rollback · **L**
- **User value:** a bad deploy rolls itself back before it becomes an incident.
- **What:** after a deploy, watch error-rate/latency/SLO burn for N minutes; on regression, mark the deploy failed and (policy-gated) trigger a rollback playbook via action-service.
- **Backend:** a post-deploy watcher (correlation-service) → deploy status + optional rollback under the remediation policy.
- **Frontend:** live "verifying deploy…" state → pass/fail; rollback approval card.

### E4. Change-failure linking · **S**
- **User value:** every incident knows which deploy likely caused it.
- **What:** correlate incidents to the preceding deploy on the same service; show "likely caused by v1.4.2".
- **Backend:** join incident start time ↔ recent deploys.
- **Frontend:** "caused by deploy" chip on incidents + failure rate on deploys.

### E5. Multi-SCM / generic CI webhooks · **M**
- **User value:** works with GitLab, Bitbucket, or any CI, not just GitHub.
- **What:** generic deploy-event webhook + provider adapters (GitLab, Bitbucket, generic JSON).
- **Backend:** `/api/v1/webhooks/{provider}` with per-provider signature verification.
- **Frontend:** integration setup docs per provider.

### E6. Gate policy editor · **S**
- **User value:** teams decide what blocks a deploy without editing code.
- **What:** per-service gate policy (which SLOs/error thresholds block), edited in-app.
- **Backend:** `deploy_gate_policies` consumed by `/slo/evaluate-pr`.
- **Frontend:** policy editor per service.

## 4. Market-ready DoD

- Deploy markers appear on all time-series views and link to the change.
- DORA metrics are first-class; incidents link back to the deploy that caused them.
- A regressing deploy is detected and can auto-rollback under policy; gates work across SCMs with an editable policy.

## 5. Suggested sequence

E1 (markers — highest daily value) → E4 (change-failure link) → E2 (DORA) → E6 (policy) → E3 (auto-rollback) → E5 (multi-SCM).
