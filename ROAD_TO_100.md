# PulseTrace — Road to 100% Market Readiness

_Author: Principal Engineering plan · Last updated: 2026-08-03_

This is the execution plan to take every feature to **100% market readiness**, with
a hard rule that **backend and frontend stay in sync**: any capability the backend
supports must be usable from the UI, and nothing ships backend-only.

Baselines: [FEATURE_READINESS.md](FEATURE_READINESS.md) (backend, ~66%) and
[UI_FEATURE_READINESS.md](UI_FEATURE_READINESS.md) (screens, ~60%). The gap between
them **is** the problem statement: several finished backends have no face.

---

## 1. What "100%" means — the readiness rubric

A feature is 100% only when it clears **all seven** dimensions. Every per-feature
plan below is written to satisfy this rubric; "Definition of Done" (DoD) maps to it.

| # | Dimension | Bar to clear |
| --- | --- | --- |
| **R1** | Functional completeness | Every intended user action works end-to-end; no stubs, no `alert()` placeholders. |
| **R2** | **Backend↔UI parity** | Every backend endpoint/capability has a UI control; every UI control hits a real endpoint. **No orphans either way.** |
| **R3** | Test coverage | Unit (logic) + integration (DB/queue) + one Playwright e2e per user-facing flow, all in CI. |
| **R4** | Scale & performance | Validated at a defined target load with published p50/p95/p99 and a throughput ceiling. |
| **R5** | Security & tenancy | Tenant-isolated, authz-gated at the right granularity, no secret leakage, inputs validated. |
| **R6** | Observability & ops | The feature emits its own metrics/traces/logs; has runbook + failure-mode behaviour. |
| **R7** | UX & docs | Loading/error/empty states, a11y baseline, and user + API docs. |

## 2. Principal-level operating principles

1. **Parity is a release gate.** A PR that adds a backend endpoint without its UI (or vice-versa) does not merge. CI enforces this via the capability registry (F0.1).
2. **Vertical slices, not layers.** Each feature is finished as `backend + UI + tests + docs` in one stream, so nothing sits half-exposed.
3. **Prove at scale before polishing.** R4 gates the pillars; we validate the ingestion path (F0.2) before gold-plating screens.
4. **Every endpoint ships with an e2e test and a UI control in the same PR.** This is how parity stops regressing.
5. **Fail safe, surface honestly.** Degraded states (LLM down, Kafka down, no cluster) render as explicit UI states, never as fake success.

---

## 3. Cross-cutting foundations (Wave 1 — unblock everything else)

These raise many features at once and are prerequisites for the parity gates.

### F0.1 — Backend↔UI capability registry + parity CI gate  · effort M
- **Backend:** generate an OpenAPI spec from the gateway routes (or hand-maintain `api/openapi.yaml`). Tag each operation with a `x-ui-surface` (the component/route that consumes it) or `x-ui: none` (ingest/webhook internal).
- **Frontend:** a generated `src/lib/apiClient.ts` from the spec (typed client), replacing ad-hoc `fetchWithAuth` string literals and the `any[]` state.
- **CI gate:** a script diffs (a) endpoints in the spec vs endpoints referenced in `frontend/src` and (b) UI calls vs real routes. Any endpoint without a UI surface (and not `x-ui: none`) **fails the build**. This is the mechanism that keeps R2 true forever.
- **DoD:** the parity matrix (§4) is generated, not hand-maintained; build red on any new orphan.

### F0.2 — Load & scale validation harness  · effort L · ✅ delivered
- ✅ Sustained, multi-protocol k6 ingestion test in [`scripts/load/`](scripts/load/) (`ingest-load.js`): native logs + OTLP logs + Datadog + Splunk at a configurable aggregate arrival rate, with **per-protocol** p95/p99 thresholds that fail the build on regression.
- ✅ Downstream back-pressure sampling (`collect-infra-metrics.sh`): Kafka consumer-group lag, ClickHouse active parts/merges, gateway + log-service CPU/mem; merged into [`PERF_BASELINE.md`](PERF_BASELINE.md) (p50/p95/p99 + a documented method for finding the throughput ceiling). The results block is machine-written by `run-baseline.sh`, never by hand.
- ✅ Scale stage in CI: per-PR fast gate (`ci.yml` → `load-test`, native path) + a **scheduled** deep run (`.github/workflows/scale-baseline.yml`, weekly + on-demand) that uploads the baseline as an artifact.
- **Unblocks R4 for every pillar** — most pillar scores are currently capped by "scale unproven."

### F0.3 — Structural tenant isolation  · effort L · ✅ delivered (ClickHouse read path); forward path documented
- ✅ Enforced invariant on the ClickHouse read path (the raw-SQL choke point): `clickHouseClient.queryScoped` **fails closed** on an empty tenant or a tenant-scoped table read with no tenant predicate, and injects the tenant bind param from one trusted source. All 11 CH read sites migrated onto it.
- ✅ Static ratchet (`TestNoRawTenantTableReads`) fails the build if any handler bypasses the guard with a raw `.query()` on a tenant table — "forgot the filter" is now a build failure, not a leak. Proven to catch a planted violation. Unit tests cover the guard.
- ✅ Audit ([TENANT_ISOLATION.md](TENANT_ISOLATION.md)): documented how every store (Postgres/ClickHouse/Quickwit/Neo4j) is scoped, and confirmed **no acute leak** — the live read paths already filter by tenant, and server-side tenant resolution ignores forged `X-Tenant-ID` (now covered by the extended cross-tenant e2e's header-spoof test).
- ✅ ClickHouse partitioning: app-owned tables (`rum_events`, `synthetic_results`) are already `PARTITION BY TenantID`. The collector-owned `otel_*` tables can't have their keys changed from our migrations (documented **honest limitation** + forward path: a tenant-keyed materialized view, deferred, tracked with F19 deletion).
- ↪ Remaining (deferred): the `shared/` `TenantScopedDB` wrapper for the Postgres read paths across correlation/topology services, and the tenant-keyed MV. Postgres/Neo4j reads already filter; the wrapper is a defense-in-depth generalization of the same ratchet.
- **Raises R5 across all pillars; hard prerequisite for enterprise procurement.**

### F0.4 — Frontend platform: typed client, design system, a11y, real-time  · effort L · ✅ foundation delivered
- ✅ **Typed API client** ([`src/lib/api/`](frontend/src/lib/api/)): `api.get/post/…` and envelope-aware `getData/list` return typed data and throw a typed `ApiError` (status + server message), replacing ad-hoc `fetchWithAuth(...).then(r=>r.json())` and `json.data || []`. Domain types (`Role`, envelopes) grow as screens migrate.
- ✅ **Design-system primitives** ([`src/components/ui/`](frontend/src/components/ui/)): `StateBoundary` (one accessible loading/error/empty/retry surface), `ToastProvider`/`useToast` (non-blocking notifications — **kills `alert()`**), `ConfirmDialog` (accessible, focus-managed — **replaces `confirm()`**). Mounted app-wide via `ToastProvider` in the root layout.
- ✅ **Shared live-update primitive** ([`useApiResource`](frontend/src/lib/hooks/useApiResource.ts)): typed `{data,error,loading,refetch}` with optional silent polling — replaces the `useState<any[]> + useEffect(fetch)` boilerplate and powers tail/streaming.
- ✅ **a11y baseline in CI**: `@axe-core/playwright` + [`tests/e2e/a11y.spec.ts`](frontend/tests/e2e/a11y.spec.ts) scans dashboard/settings/incidents/explorer/login for **critical** WCAG 2.1 A/AA violations (bar tightens to `serious` as debt burns down).
- ✅ **Proof of pattern**: `RolesPanel` fully migrated onto client + hook + all three primitives (removed `alert()`×2, `confirm()`, inline states; net −2 lint errors).
- ↪ Remaining (Wave 2, screen-by-screen): migrate the other screens off `fetchWithAuth`/`useState<any[]>`/`alert()` (the pre-existing frontend **lint debt of ~85 `no-explicit-any` errors** lives in these unmigrated screens — the platform is the tool to burn it down); a shared `DataTable`; SSE where polling isn't enough.

### F0.5 — Causal-AI evaluation harness  · effort L · ✅ delivered
- ✅ Labelled fixture set of 11 incidents with known root causes ([`shared/causal/eval_fixtures.go`](shared/causal/eval_fixtures.go)) and a reusable scorer ([`eval.go`](shared/causal/eval.go)) that scores **any** `Analyzer` (the rule-based fallback in CI; LLM providers when a key is present) on four dimensions: root-cause **service** identified, remediation **playbook** correct, **confidence** floor, **narrative** on-topic.
- ✅ CI gate ([`eval_test.go`](shared/causal/eval_test.go), runs in the `shared` job): fails the build if the deterministic accuracy regresses below thresholds (overall ≥ 85%, root-service ≥ 85%, playbook ≥ 95%). Current rule-based score: **90.9%** — published in [CAUSAL_EVAL.md](CAUSAL_EVAL.md).
- ✅ Honest headroom: one fixture (root cause behind an *undeclared* dependency) is a deliberate deterministic miss the graph-walk can't solve — the gap an LLM narrative closes — so the headline number isn't a fixture-designed-to-pass.
- **This is the gate that lets "AI RCA" be sold as such** rather than "an LLM call that runs."

---

## 4. Backend ↔ Frontend parity matrix (the sync contract)

Every row must end at **✅ UI shipped**. Bold rows are today's orphans — the
highest-priority parity work.

| Capability | Backend | UI today | Action to reach parity |
| --- | :---: | --- | --- |
| Logs / Traces / Metrics / RUM / Synthetics / Topology / Catalog / Errors | ✅ | ✅ screens | Depth work (see §5), not parity. |
| Roles / Policies / Rate-limits / Users / Alert-rules / Audit / Billing | ✅ | ✅ Settings panels | Minor: rotation button (below). |
| Self-healing approve / reject / dry-run | ✅ | ✅ Incidents remediation panel | ✅ Done (F1). |
| SLO / error-budget / burn-rate | ✅ | ✅ SLOs screen | ✅ Done (F2). |
| Alert delivery channels (Slack/PD/Opsgenie/webhook) | ✅ | ✅ Channels panel + test-send | ✅ Done (F3) — per-tenant, DB-backed, encrypted. |
| Ingestion-key rotation | ✅ | ✅ Keys panel (list/create/rotate/revoke) | ✅ Done (F4). |
| Deploy gates (shift-left) | ✅ | ✅ live gate feed | ✅ Done (F5) — wired, not removed. |
| AI-SRE remediation execute | ✅ | ✅ confirm→run→result | ✅ Done (F1) — `alert()` replaced. |
| Anomaly detection config/thresholds | ⚠️ engine only | ❌ none | No config endpoint exists yet — needs backend first (F14, reclassified to Wave 3). Not a parity orphan. |
| Data retention/deletion (GDPR) | ✅ | ✅ Settings → Data & Privacy | ✅ Done (F19). |
| Usage vs quota | ✅ | ✅ Billing & Usage panel | `GET /api/v1/usage` already consumed (BillingPanel). Depth (quota bars/projection) is Wave 3, not a parity orphan. |
| Alerts (raw stream) / alert detail | ✅ | ✅ Alerts screen | ✅ Done. |
| Tenant plan override / user role edit | ✅ | ✅ Billing / Users | ✅ Done (F17/F18). |
| Topology downstream (blast radius) / log permalink | ✅ | ✅ Topology / Explorer | ✅ Done (F13/F6). |

---

## 5. Per-feature completion plans

Format per feature: **current (BE→UI) → 100%**, backend tasks, frontend tasks,
tests, DoD, effort. Ordered by leverage (parity gaps first).

### F1 — Self-healing remediation (flagship) · 60→**100** BE, 20→**100** UI · effort L · ✅ UI parity delivered
The backend (`correlation-service/internal/handler/playbook_handler.go`: approve/reject/dry-run + risk-tier authz) had no screen — the incident detail showed hardcoded fake runbooks. Now a real, policy-aware panel.
- ✅ **Frontend** ([`RemediationPanel`](frontend/src/components/Incidents/RemediationPanel.tsx) on a rewritten [`IncidentsView`](frontend/src/components/Incidents/IncidentsView.tsx)): shows the proposed playbook with live status (SUGGESTED/DRY_RUN/PENDING_APPROVAL/EXECUTING/EXECUTED/FAILED/REJECTED), **Dry-run** (renders the plan/output), **Approve** (confirm→run) and **Reject** (with reason), approver/audit trail, and the **policy posture** read from `GET /api/v1/remediation/policy` — Approve is hidden/disabled when execution is off, so there's never a button that silently does nothing. High-risk role gating is enforced server-side and surfaced as a clear error. IncidentsView also now renders the real causal narrative + confidence + model and the incident **timeline** — consuming and delisting all six F1 parity routes.
- ✅ **AI-SRE execute** ([`app/page.tsx`](frontend/src/app/page.tsx)): the four blocking `alert()`s are replaced with an accessible **confirm→run→result** flow (`ConfirmDialog` + toasts + a chat result line); transport failures are reported honestly, never as fake success.
- ✅ **Tests:** Playwright (`incidents.spec`) — detail + policy-aware remediation panel + dry-run; the risk-tier authorization is already covered by [`shared/remediation/authz_test.go`](shared/remediation/authz_test.go) (non-elevated role cannot approve high-risk).
- ↪ **Backend depth (deferred to Wave 3 pillar work):** post-remediation verification + auto-revert, action types beyond restart/scale/rollback, a first-class remediation-history endpoint, and the in-cluster operator RBAC manifest. These deepen the flagship but are **beyond the parity orphan** (which was "capability with no UI") — R2 is closed.
- **DoD (parity):** an on-call user can dry-run, approve/reject and audit a fix from the UI; degraded/execution-disabled states shown honestly; R1–R3/R5/R7 met for the UI slice.

### F2 — SLO / error-budget / burn-rate · 66→**100** BE, 10→**100** UI · effort M · ✅ delivered
Real engine (`burn_rate_alerter.go`, `slo_worker.go`, `slo_repository.go`) had **no view**.
- ✅ **Frontend** (new **SLOs** nav screen — [`SLOView`](frontend/src/components/SLO/SLOView.tsx), route [`/slo`](frontend/src/app/slo/page.tsx)): per-service cards with **budget-remaining gauge**, current SLI, **burn-rate** (× multiplier), status (healthy/warning/critical), and an SLI **trend sparkline**; create (service/target %/SLI type/window) and delete SLOs; a live **Budget Alerts** feed of burn-rate breaches. Polls the dashboard + alerts. Consumes and delists all five F2 routes (definitions GET/POST/DELETE, dashboard, budget-alerts).
- ✅ **Tests:** Playwright (`slo.spec`) — dashboard renders the seeded objective + budget gauge + alerts section; create-SLO round-trip. Budget math is already unit-tested in the engine. Seed now provisions an SLO for `payment-service`.
- ↪ Deferred (depth): multi-window burn-rate alert config, per-service SLO surfaced on the Services screen, edit-in-place. Not needed to close the parity orphan.
- **DoD (parity):** a user defines an SLO and sees budget burn + breach alerts from the UI; R1–R3/R5/R7 met.

### F3 — Alert delivery channels · 80→**100** BE, 15→**100** UI · effort M · ✅ delivered
Slack/PD/Opsgenie/webhook + auto-resolve were real but **env-var-only**. Now per-tenant, DB-backed, UI-managed.
- ✅ **Backend:** channel config moved to a tenant-scoped `notification_channels` table ([migration 018](gateway-service/migrations/018_create_notification_channels.sql)) with **AES-256-GCM-encrypted secrets** at rest ([channels package](notification-service/internal/channels/) — crypto/model/repository/deliver/handler; **fails closed** without `CHANNEL_ENCRYPTION_KEY`, never stores plaintext). CRUD + **test-send** HTTP API on notification-service (:8086), gateway-proxied at `/api/v1/notification-channels` (**admin-gated** by RBAC, tenant-scoped from the JWT). The worker now delivers to env globals **and** each tenant's DB channels (additive, backward-compatible); `NotificationEvent` carries `TenantID` (set by the correlator) so events route to the right tenant. Delivery is one shared config-driven path (Slack/email/PagerDuty/Opsgenie/webhook-with-HMAC) used by both live dispatch and test-send, so behavior can't drift.
- ✅ **Frontend** ([`ChannelsPanel`](frontend/src/components/Settings/ChannelsPanel.tsx) in Settings → Alert Channels): add/edit/remove per type with type-specific fields, **Send test**, enable/disable; secrets are **write-only** (shown as "configured", blank-to-keep on edit). Replaces the old env-only info block.
- ✅ **Tests:** Go ([channels_test.go](notification-service/internal/channels/channels_test.go), green against Postgres) — AES round-trip + fresh nonce, redaction, delivery incl. **webhook HMAC verification**, disabled-is-no-op, and DB repo: encrypt-at-rest, decrypt-for-delivery, redact-for-API, blank-secret-preserving update, tenant isolation. Playwright (`settings.spec`) — lists the seeded channel + add flow. Compose wires `DATABASE_URL`/`CHANNEL_ENCRYPTION_KEY`/`NOTIFICATION_SERVICE_URL`; seed provisions a demo channel.
- ↪ Deferred (depth): routing/escalation policies (severity/service→channel, on-call schedules) and de-dup windows — the delivery + config substrate is now in place for them.
- **DoD:** an admin configures and tests on-call delivery from the UI without touching env; secrets are never rendered back; R1–R3/R5/R7 met.

### F4 — Ingestion-key lifecycle · 84→**100** BE, 40→**100** UI · effort S · ✅ delivered
Rotation shipped in the backend (grace window, `replaced_by`); the Settings "API Keys" tab was a hardcoded placeholder (fake `pt_live_***` key, dead buttons). Now a real panel on the F0.4 platform.
- ✅ **Frontend** ([`IngestionKeysPanel`](frontend/src/components/Settings/IngestionKeysPanel.tsx)): lists real keys with status (Active / Retiring-at-`revoked_at` grace / Revoked), scope + tier + last-used and `replaced_by` lineage; **Generate** (name/scope/tier), **Rotate** with a grace-period picker, **Revoke** via accessible confirm; a one-time plaintext **reveal modal** (copy-to-clipboard) shared by create *and* rotate, matching the "shown once" server contract. Replaces the placeholder; typed client + `useApiResource` + `StateBoundary`/`ConfirmDialog`/`Toast`, no `any`.
- ✅ **Tests:** Go DB-backed integration ([`ingestion_keys_test.go`](gateway-service/internal/auth/ingestion_keys_test.go)) proving the grace contract — **both keys valid during the window, predecessor dies immediately on grace-0**, plus lineage, future-dated revocation, over-max/malformed grace → 400, unknown key → 404; Playwright e2e (settings.spec) create→one-time-reveal→appears→rotate. Seed now provisions a demo key.
- ↪ **Backend (deferred, optional):** auto-rotation scheduler + expiry reminders — not required to close parity.
- ✅ **DoD:** full key lifecycle (mint/list/rotate/revoke) is UI-drivable; parity registry's two F4 orphans delisted; R2 closed for this row.

### F5 — Deploy gates (shift-left) · partial→**100** · effort M · ✅ delivered (wired, not removed)
**Decision: wired.** The gate was worse than a placeholder — the webhook wasn't even registered, used a hardcoded `localhost:8083`, and persisted nothing. Now a real feature.
- ✅ **Backend:** the GitHub webhook is registered (public + **HMAC-verified** when `GITHUB_WEBHOOK_SECRET` is set), evaluates each PR via correlation-service's SLO-risk evaluator (**fails open** on evaluator outage — advisory, not a hard deploy dependency), and **persists every verdict** ([migration 017 `deploy_gates`](gateway-service/migrations/017_create_deploy_gates.sql)). New tenant-scoped read endpoint `GET /api/v1/deployments/gates` ([`github_webhook.go`](gateway-service/internal/handler/github_webhook.go)). Fixed the hardcoded correlation URL.
- ✅ **Frontend** ([`DeploymentsView`](frontend/src/components/Deployments/DeploymentsView.tsx)): the placeholder is replaced by the **real gate feed** (typed client + `useApiResource` + `StateBoundary`, polled), with PR links, decision badges, and a webhook-setup helper. Dead buttons removed — read-only by design (the verdict is returned to GitHub as a commit status, so there's no override to fake).
- ✅ **Tests:** Go DB-backed ([`github_webhook_test.go`](gateway-service/internal/handler/github_webhook_test.go)) — BLOCK persists + lists, HMAC verification, evaluator-down fail-open (all green against Postgres); Playwright (`deployments.spec`) — feed + webhook-setup URL. Seed posts a demo PR through the webhook.
- ↪ **Documented limitation:** the webhook is unauthenticated (GitHub can't send a JWT), so verdicts land in the `default` tenant — correct for single-tenant on-prem; a SaaS repo→tenant mapping is a follow-up.
- **DoD:** the screen shows live gate data, no placeholder remains; R1–R3/R5/R7 met.

### F6 — Logs (Explorer) · 78→**100** BE, 78→**100** UI · effort M
- **Backend:** validate at cardinality (F0.2); retention/reindex ops; live-tail endpoint.
- **Frontend:** field histograms, live tail, surrounding-context view, shareable query URLs.
- **DoD:** scale numbers published; R4 met; power-user query UX.

### F7 — Traces · 75→**100** BE, 78→**100** UI · effort M
- **Backend:** trace↔logs correlation by trace_id at query time; exemplars from metrics; scale proof.
- **Frontend:** span-detail deep links, trace→logs jump, service-map-from-traces.
- **DoD:** a user pivots trace→logs→metrics without leaving context.

### F8 — Metrics · 66→**100** BE, 68→**100** UI · effort M
- **Backend:** finish per-service `/metrics` handlers (roadmap gap); a query API with functions (rate/quantile/aggregations).
- **Frontend:** multi-series charts, saveable dashboards, alert-from-chart, unit-aware axes.
- **DoD:** metrics is a first-class explorable pillar, not single-series.

### F9 — RUM · 70→**100** BE, 62→**100** UI · effort M
- **Backend:** sampling controls, session stitching, geo/device enrichment; versioned browser SDK.
- **Frontend:** **trend charts** (not point-in-time cards), session detail, geo/device breakdowns, error→trace correlation.
- **DoD:** RUM tells a time-series + session story; public-token rotation (F4) surfaced.

### F10 — Synthetics · 68→**100** BE, 66→**100** UI · effort M
- **Backend:** multi-step/browser checks, richer assertions, multi-region probing, failure→alert wiring.
- **Frontend:** step editor, assertion builder, latency-trend charts, per-test alert config.
- **DoD:** a user builds a multi-step check that pages on failure.

### F11 — Error tracking · 62→**100** BE, 64→**100** UI · effort M
- **Backend:** source-map de-minification, release/regression tracking, alert-on-new-group.
- **Frontend:** de-minified stacktraces, occurrence timeline, assignment/ownership, release view.
- **DoD:** a frontend error resolves to real source + notifies on regression.

### F12 — Continuous profiling · 55→**100** BE, 55→**100** UI · effort M
- **Backend:** flamegraph diffing/regression detection surfaced from PulseTrace (less raw Pyroscope passthrough); overhead controls.
- **Frontend:** in-product flamegraph + diff view, loading/empty states, regression callouts.
- **DoD:** profiling is a product surface, not an embedded tool.

### F13 — Topology & Catalog · 65→**100** BE, 70→**100** UI · effort M
- **Backend:** deeper auto-discovery, ownership metadata, tenant-scope hardening (F0.3), scale.
- **Frontend:** filter/focus/collapse on large graphs, health overlay, catalog scorecards + search.
- **DoD:** topology is usable at 100s of services; catalog carries ownership/SLO.

### F14 — Anomaly detection · 68→**100** BE, 0→**100** UI · effort M
- **Backend:** seasonality-aware baselines, multi-metric, alert-fatigue controls.
- **Frontend:** anomaly config/thresholds UI + anomaly overlay on metric/service charts (currently no UI at all).
- **DoD:** users tune sensitivity and see flagged anomalies inline.

### F15 — Causal AI / RCA · 62→**100** BE, 58→**100** UI · effort L
- **Backend:** the eval harness (F0.5), hallucination guardrails, cost/latency budgets, provider health surfaced.
- **Frontend:** richer causal-narrative rendering on Incidents (chain viz, evidence, confidence), streaming chat, conversation history; replace `alert()` execute (shared with F1).
- **DoD:** a measured accuracy number ships; the narrative is legible and actionable.

### F16 — Metering, quota & usage · 72→**100** BE, partial→**100** UI · effort M
- **Backend:** scale-accurate metering + reconciliation, overage handling, per-tenant usage API.
- **Frontend:** **Usage dashboard** (ingest volume vs plan, quota bars, projected overage) in Settings/Onboarding.
- **DoD:** a customer sees exactly what they're using vs paying for.

### F17 — Billing & revenue · 58→**100** BE, 55→**100** UI · effort L
- **Backend:** proration, dunning/failed-payment recovery, invoicing, tax, metered-usage→invoice (from F16), validated beyond Stripe test mode.
- **Frontend:** in-app plan compare/upgrade/downgrade, invoice history, payment-method management (beyond portal redirect).
- **DoD:** the full self-serve money path works in-product; R1–R7.

### F18 — Auth, RBAC/ABAC · 75/78→**100** · effort M
- **Backend:** MFA, SAML/SCIM SSO, session revocation, password reset.
- **Frontend:** MFA enrolment, session/device management, a **policy-authoring UX** (guided ABAC builder, not raw expr strings), password-reset flow.
- **DoD:** enterprise SSO/MFA + self-service policy editing.

### F19 — Data retention & deletion (GDPR/SOC2) · 70→**100** BE, 0→**100** UI · effort M · ✅ delivered
The gateway can purge a tenant across every store (ClickHouse/Quickwit/Neo4j + derived Postgres) and fully close an account, but there was no UI.
- ✅ **Frontend** (Settings → **Data & Privacy** — [`DataPrivacyPanel`](frontend/src/components/Settings/DataPrivacyPanel.tsx)): a danger-zone with **Purge telemetry data** (keeps the account) and **Close account** (full offboarding), each gated by an accessible **type-your-tenant-id** confirm modal matching the server's `{confirm}` contract; the per-store `Result` renders as a **deletion certificate** (✓ steps / ✗ errors). Consumes and delists `POST /admin/tenant/purge-data` (F19) and `POST /admin/tenant/close` (F17); `DELETE /api/v1/topology/tenant` reclassified to `uiNone` (the gateway purger fans out to topology internally, it's not a UI call).
- ✅ **Tests:** Playwright (`settings.spec`) — the tab renders both actions and gates the destructive button behind the type-to-confirm modal (the test cancels; it never wipes the shared seeded tenant).
- ↪ Deferred (depth): async-job status/retries and a downloadable certificate file. The synchronous per-store result already evidences the deletion.
- **DoD (parity):** a data-subject deletion is self-serve, confirmed, and evidenced from the UI; R1–R3/R5/R7 met.

### F20 — Audit logging · 72→**100** · effort S
- **Backend:** hash-chained tamper-evidence, export API, immutable retention.
- **Frontend:** audit panel exists — add export + verify-integrity action.
- **DoD:** audit trail is tamper-evident and exportable for compliance.

### F21 — Infra, CI/CD, secrets, DR · 40–70→**100** · effort L
- **Backend/infra:** wire External Secrets to a concrete backend end-to-end; add staging deploy + security scanning to CI; enforce perf baselines from F0.2; production-cluster soak test; then a first-class **DR/multi-region** design (currently 15% — deferred; revisit at first customer needing it).
- **DoD:** reproducible prod deploy with secrets managed, perf-gated CI, documented DR posture.

---

## 6. Sequenced roadmap (waves)

Ordered for maximum leverage and to keep BE/UI in lockstep.

| Wave | Theme | Contents | Exit criteria |
| --- | --- | --- | --- |
| **1** ✅ | Foundations | F0.1 parity gate, F0.4 FE platform, F0.2 load harness, F0.3 isolation, F0.5 eval harness | ✅ **All five delivered.** Parity CI green; perf harness + baseline; structural tenant isolation (+2 live leaks fixed); typed FE platform; causal-AI eval gate at 90.9%. |
| **2** ✅ | **Close the parity orphans** | ✅ F4 keys, F1 remediation UI, F2 SLO screen, F19 deletion UI, F17 plan, F18 role edit, Alerts screen, F13 downstream, F6 log permalink | ✅ **Parity gate at 100% — 0 registry orphans; every backend route has a UI (R2 = 100%).** Note: F3 channels & F14 anomaly config have **no backend endpoints** (env/engine-only) so they are not parity orphans — reclassified to Wave 3 (need backend built first). F16 usage already consumed. F5 deploy-gates: **wired** (real feed + persistence + HMAC webhook). |
| **3** | Pillar depth | F6–F13 (logs/traces/metrics/RUM/synthetics/errors/profiling/topology), F15 causal narrative | Pillars competitive; scale-validated. |
| **4** | Revenue & enterprise | F17 billing, F18 SSO/MFA/policy UX, F20 audit, F21 infra/DR | Self-serve money path + enterprise procurement bar met. |

**Guiding gate between waves:** Wave 2 cannot be declared done while any row in the
§4 parity matrix is not ✅ — that is the literal "backend and frontend in sync" bar.

## 7. Risks & mitigations

- **Scale surprises (highest).** ClickHouse/Kafka may not hit targets → F0.2 first, before UI polish; design for horizontal sharding early.
- **Causal-AI quality unprovable.** If eval (F0.5) shows weak accuracy, reposition as "assisted RCA" and lean on the deterministic chain until it improves.
- **Parity gate friction.** The CI gate (F0.1) will block PRs — that's intended; pair it with the generated client so adding UI for a new endpoint is cheap.
- **Scope creep per feature.** Enforce the rubric DoD as the exit; resist gold-plating a pillar past parity before all orphans are closed (Wave 2 > Wave 3).

---

### One-line summary
Ship **Wave 1 foundations** (parity CI + load + isolation + FE platform + eval),
then **Wave 2 closes every backend-without-UI orphan** — after which backend and
frontend are provably in sync — then deepen pillars and complete the revenue/
enterprise surface to reach 100% against the seven-dimension rubric.
