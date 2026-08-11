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
| Anomaly detection config/thresholds | ✅ | ✅ Settings → Anomalies | ✅ Done (F14) — per-tenant tuning, live-applied. |
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

### F6 — Logs (Explorer) · 78→**100** BE, 78→**100** UI · effort M · ✅ delivered
Facets, volume histogram, regex, saved searches and live tail shipped in Wave 2; this slice closes the two remaining power-user gaps.
- ✅ **Surrounding-context view:** new tenant-scoped backend endpoint `GET /api/v1/logs/{id}/context?before=N&after=N` ([`log_handler.go`](log-service/internal/handler/log_handler.go)) — resolves the anchor server-side (never trusts a client-supplied service/timestamp), then fetches N neighbours per side on the **same service** by timestamp, de-dupes the anchor and any tie-timestamp log, and clamps the window (default 25/side, max 200). The Explorer detail drawer gains a **View in context** action opening an overlay that renders `before · anchor(highlighted) · after` ([`ExplorerView.tsx`](frontend/src/components/Explorer/ExplorerView.tsx)).
- ✅ **Shareable query URLs:** the full search state (box text, regex toggle, time window) round-trips through the URL (`?q=&regex=&range=`) — a **Share** button copies a link that reproduces the exact search (not just the pre-existing single-log `?log=` permalink), and the state hydrates from the URL on mount.
- ✅ **Tests:** Go unit — `clampContextWindow` bounds, `buildContextQuery` tenant/service scoping + range direction, `assembleContext` chronological ordering + cross-side de-dup + anchor removal; Playwright (`explorer.spec`) — shareable-link hydration, view-in-context overlay resolves to a real state. Ingestion scale is validated by F0.2.
- ↪ Deferred (depth): a dedicated server-side live-tail cursor endpoint (client polling covers it today) and retention/reindex ops.
- **DoD:** power-user query UX (context + shareable searches) complete; R1–R3/R5/R7 met for the UI slice; R4 covered by F0.2.

### F7 — Traces · 75→**100** BE, 78→**100** UI · effort M · ✅ correlation delivered
The trace↔logs correlation backend already worked (log-service indexes `trace_id`; the waterfall shows per-trace correlated logs inline). The gap was the **bidirectional UI pivot** — the Explorer's "View Trace" button was literally dead, and there was no jump from a trace to the full Explorer.
- ✅ **log→trace pivot** ([`ExplorerView.tsx`](frontend/src/components/Explorer/ExplorerView.tsx)): the previously-dead "View Trace" button now deep-links to `/traces?trace=<id>`, which `TracesView` already resolves into the waterfall.
- ✅ **trace→logs jump** ([`TraceWaterfall.tsx`](frontend/src/components/Traces/TraceWaterfall.tsx)): an **Open in Explorer** action on the correlated-logs panel navigates to `/explorer?q=trace_id:"<id>"`, **reusing F6's shareable-query URL** so the pivot lands on a fully-scoped, further-refinable search rather than a dead-end preview.
- ✅ **Tests:** Playwright — `explorer.spec` asserts the log→trace deep link (seeded logs carry `trace_id`); `traces.spec` opens a trace, selects a span, and asserts the trace→logs pivot lands on `/explorer?q=…trace_id`.
- ↪ Deferred (depth): span-level log correlation (the Quickwit logs index carries `trace_id` but not `span_id`, so span-precise filtering would query a non-existent field — an honest limitation, not shipped as a broken control), metric exemplars, and service-map-from-traces.
- **DoD:** a user pivots trace→logs and logs→trace without leaving context; the profiler pivot (span→profile) already existed. R1–R3/R5/R7 met for the correlation slice.

### F8 — Metrics · 66→**100** BE, 68→**100** UI · effort M · ✅ query functions delivered
The native OTLP metrics pillar (ClickHouse-backed catalog + per-service multi-series) already existed but was **avg-only** — its own code flagged "not a substitute for true rate()." This slice adds the query-function layer and unit-aware rendering.
- ✅ **Backend query functions** ([`metrics_handler.go`](gateway-service/internal/handler/metrics_handler.go)): `GET /api/v1/metrics/query` now takes `fn=avg|min|max|sum|rate|p50|p90|p95|p99`, resolved by a pure `metricAggExpr` builder validated against a closed allowlist (unknown `fn` → 400, never a silent avg). **rate()** is the per-second monotonic-counter increase over each bucket (`greatest(max−min,0)/bucketSeconds`, reset-safe); **p50–p99** are ClickHouse `quantile` over the bucket's datapoints. Bucket widths are tracked alongside the bucket expressions so rate's denominator stays correct per interval.
- ✅ **Frontend** ([`MetricsView.tsx`](frontend/src/components/Metrics/MetricsView.tsx)): a **function selector** (avg/rate/min/max/sum/p50–p99) alongside the interval picker, and **unit-aware axes** — the Y-axis formats magnitudes (k/M/G) and labels the metric's OTLP unit (`By`, `ms`, …), turning into `<unit>/s` under rate. Multi-series (per-service lines) already existed.
- ✅ **Tests:** Go unit ([`metrics_handler_test.go`](gateway-service/internal/handler/metrics_handler_test.go)) — every supported `fn` → expression, rate's bucket-width denominator + reset clamp, invalid-`fn` rejection, and a lock-step check that every bucketed interval has a rate denominator. Playwright (`metrics.spec`) — catalog lists seeded metrics; selecting `rate` on the counter applies without error. The **seed now emits OTLP metrics** (gauge + monotonic counter + byte-unit gauge per service) so the pillar renders end-to-end.
- ↪ Deferred (depth): saveable dashboards, alert-from-chart, and histogram/summary percentile math (bucket-aware, its own endpoint).
- **DoD:** metrics is a first-class explorable pillar with real aggregation functions and unit-aware charts, not single-series avg; R1–R3/R5/R7 met.

### F9 — RUM · 70→**100** BE, 62→**100** UI · effort M · ✅ time-series + session story delivered
RUM had point-in-time web-vitals cards + a recent-errors table (error→trace already wired) but no time-series and no session story. This slice adds both.
- ✅ **Backend** ([`rum_handler.go`](gateway-service/internal/handler/rum_handler.go)): three tenant-scoped read endpoints — `GET /api/v1/rum/trends` (time-bucketed **p75 web-vital trends**, reusing the metric pillar's shared bucketing), `GET /api/v1/rum/sessions` (**session stitching**: one row per visit with entry path, page-view/error counts, duration, and a parsed device label), `GET /api/v1/rum/devices` (**device/browser/OS breakdown**). User-Agent parsing is a single pure `classifyUserAgent` used by both sessions and the breakdown. Ingest now honours a client-supplied event `timestamp` (RUM is inherently client-timed), enabling real trends.
- ✅ **Frontend** ([`RUMView.tsx`](frontend/src/components/RUM/RUMView.tsx)): a **Web Vitals Trend** line chart (per-vital p75 over time, not a single card), a **device/browser/OS breakdown** with share bars, a **User Sessions** table, and the previously-dead time-range selector now re-scopes the windowed panels. error→trace correlation and ingestion-key rotation (F4) were already surfaced.
- ✅ **Tests:** Go unit ([`rum_handler_test.go`](gateway-service/internal/handler/rum_handler_test.go)) — `classifyUserAgent` across Chrome/Safari/Edge/Firefox × Windows/iOS/Android/macOS × desktop/mobile/tablet (incl. the Edge-embeds-Chrome and iPad-is-tablet traps) + deterministic `sortedBreakdown` ordering. Playwright (`rum.spec`) — trend/breakdown/session sections render, time-range switch re-scopes. The **seed now emits RUM** (10 sessions of page-views/vitals/errors across 6 UAs, timestamped over ~6h) so the pillar renders end-to-end.
- ↪ Deferred (depth): **geo** enrichment (needs client-IP capture + a geo DB — honest limitation, not shipped as an empty control), sampling controls, a versioned browser SDK, and per-session event-timeline drill-in.
- **DoD:** RUM tells a time-series + session story from the UI; R1–R3/R5/R7 met.

### F10 — Synthetics · 68→**100** BE, 66→**100** UI · effort M · ✅ multi-step + assertions + paging delivered
The prober was single-URL / 2xx-only with no alerting. This slice makes it a real synthetic-check product.
- ✅ **Multi-step checks + assertions** ([`synthetics_handler.go`](gateway-service/internal/handler/synthetics_handler.go)): a check is now an ordered list of steps (method + URL), each with an **assertion** — expected status, max-latency SLA, and body-contains. The worker runs steps sequentially and **stops at the first failing step** (later steps depend on earlier ones). Assertions are evaluated by a pure `evaluateAssertion` (status-before-latency-before-body). Persisted as a JSONB `spec` on `synthetic_targets`; legacy single-URL rows still run as a one-step 2xx GET (fully backward-compatible). Every step URL is SSRF-validated before persistence and again before probing.
- ✅ **failure→alert wiring** ([`main.go`](gateway-service/cmd/main.go)): on a healthy→failing transition the worker emits an **ERROR log onto the `logs` Kafka topic**, which flows through the existing logs→alert→correlation→notification pipeline — a failed check pages on-call exactly like an application error, with **no parallel alert path**. Paging is **edge-triggered** (once per outage, not every 10s poll), and recovery re-arms it.
- ✅ **Frontend** ([`SyntheticsView.tsx`](frontend/src/components/Synthetics/SyntheticsView.tsx)): a **step editor + assertion builder** (add/remove steps, per-step method/URL/status/latency/body-contains), a **latency-trend sparkline** and **last-failure reason** per endpoint, and a new `GET /api/v1/synthetics/tests` listing so a just-created check shows before its first probe.
- ✅ **Tests:** Go unit ([`synthetics_handler_test.go`](gateway-service/internal/handler/synthetics_handler_test.go)) — `evaluateAssertion` across default-2xx/exact-status/latency-SLA/body-contains + the status-before-latency ordering, alongside the existing exhaustive SSRF suite. Playwright (`synthetics.spec`) — the seeded multi-step check lists, and the builder assembles a 2-step check with assertions. The **seed now creates a multi-step "Checkout journey" check** with assertions.
- ↪ Deferred (honest depth): multi-region probing (needs probe infra in multiple regions) and browser-based/scripted checks (needs a headless-browser runner) — the step+assertion+paging substrate is in place for both.
- **DoD:** a user builds a multi-step check with assertions that pages on failure; R1–R3/R5/R7 met.

### F11 — Error tracking · 62→**100** BE, 64→**100** UI · effort M · ✅ regression alerting + occurrence timeline delivered
Groups + fingerprinting + resolve/mute triage already existed. This slice adds the "when/how bad" signal and proactive regression notification.
- ✅ **Occurrence timeline** ([`error_tracking_handler.go`](gateway-service/internal/handler/error_tracking_handler.go)): `GET /api/v1/errors/groups/{fingerprint}/timeline` returns the time-bucketed occurrence count for a group over 24h/7d, identified by its service/operation/normalized-message triple. The path fingerprint **must match** that triple (`fingerprint(tenant, …)`), so a fabricated id can't be paired with a mismatched identity. The Explorer expands a group into an occurrence **bar chart** ([`ErrorTrackingView.tsx`](frontend/src/components/Errors/ErrorTrackingView.tsx)).
- ✅ **Regression + new-group alerting** (the DoD's "notifies on regression"): a background worker scans each tenant's recent error groups and, on a healthy→failing edge, **pages via the `logs` topic** (→ alert → correlation → notification — the same pipeline an app error uses, no parallel path). A **regression** (a resolved group recurring after its `resolved_at`) also **auto-reopens** the group so it re-enters triage; a **new** group (no triage history, first-seen inside the scan window) pages once. The decision is a pure, exhaustively-tested `classifyErrorGroup`. Triage rows are now tenant-scoped ([migration 019](gateway-service/migrations/019_error_group_tenant.sql)) so the worker fans out per tenant.
- ✅ **Tests:** Go unit ([`error_tracking_handler_test.go`](gateway-service/internal/handler/error_tracking_handler_test.go)) — `classifyErrorGroup` across regression/new/muted/open/old-untriaged, tenant-scoped fingerprint isolation, and `parseCHTime` fallbacks. Playwright (`errors.spec`) — expanding a group renders its occurrence timeline.
- ↪ Deferred (honest depth): **source-map de-minification** (needs a source-map upload/store + mapping runtime — its own subsystem; the DoD's "resolves to real source" is partially met today via the per-group **View Trace** pivot into span context), **release/version correlation** (needs a release dimension on spans), and assignment/ownership.
- **DoD:** an error's occurrence history is visible and regressions notify on-call proactively; R1–R3/R5/R7 met for the delivered slice. (Source-map de-minification tracked as the remaining depth item.)

### F12 — Continuous profiling · 55→**100** BE, 55→**100** UI · effort M · ✅ product surface delivered
The profiler was **an iframe embed of Pyroscope's own UI** — the literal "embedded tool" this row targets. Replaced with a PulseTrace-native surface.
- ✅ **Backend diff + regression detection** ([`profiler_handler.go`](gateway-service/internal/handler/profiler_handler.go)): two new endpoints read Pyroscope's render API (flamebearer JSON) **server-side** and compute the result themselves. `GET /api/v1/profiler/functions` returns the ranked **flat profile** (top functions by self-time); `GET /api/v1/profiler/diff` compares the current window against the immediately-preceding one and returns a **per-function share diff with regressions flagged**. The diff is by each function's **share** of total self-time (percentage points), so a uniform load increase doesn't read as everything regressing. These specific paths win over the `/api/v1/profiler/` catch-all proxy (mux most-specific-wins). Pyroscope being unreachable degrades to an honest empty state (profiling is advisory).
- ✅ **Frontend** ([`ContinuousProfilerView.tsx`](frontend/src/components/Profiler/ContinuousProfilerView.tsx)): the **iframe is gone**. Flat mode renders a top-functions table with self-time share bars; **Detect Regressions** mode renders a diff table (▲ red regressions / ▼ green improvements, sorted by impact) with a **regression-count callout** banner, plus loading/empty states. The trace→profile span filter (`?spanId=`) is preserved.
- ✅ **Tests:** Go unit ([`profiler_handler_test.go`](gateway-service/internal/handler/profiler_handler_test.go)) — `aggregateSelf` (incl. out-of-range name-index safety), `topFunctions` ranking/root-skip, `diffProfiles` share-based regression detection **and** the uniform-load-increase normalization (no false regressions), `buildProfilerQuery` span filter. Playwright (`profiler.spec`) — asserts **no iframe**, the flat surface renders, and the diff verdict banner appears.
- ↪ Deferred (honest depth): an interactive flame-graph render (the flat + diff tables are the product surface; a full zoomable flamegraph is additive), profiling-overhead controls, and release-tagged (git-sha) comparison rather than time-window.
- **DoD:** profiling is a PulseTrace product surface with native diff + regression callouts, not an embedded Pyroscope UI; R1–R3/R5/R7 met.

### F13 — Topology & Catalog · 65→**100** BE, 70→**100** UI · effort M · ✅ large-graph UX + SLO scorecards delivered
The graph already carried everything needed (node `state` + ownership, edge traffic/error metrics); the gap was navigating it at scale and joining reliability into the catalog. Delivered on existing endpoints (no new routes).
- ✅ **Large-graph controls** ([`TopologyView.tsx`](frontend/src/components/Topology/TopologyView.tsx)): a **search** box that dims (keeps context, doesn't hide) non-matches; **focus** — clicking a node dims all but its 1-hop neighbours; an **"Only unhealthy"** filter that keeps unhealthy services plus their immediate context; a **health-overlay legend**; and a Reset. Filtering re-derives the view from a cached laid-out base graph, so it never re-runs dagre — responsive at 100s of nodes. Root-cause highlighting was refactored to a `highlightIds` set the same derivation owns, so it composes with the filters instead of clobbering node state.
- ✅ **Catalog scorecards + search** ([`ServiceCatalog.tsx`](frontend/src/components/Catalog/ServiceCatalog.tsx)): the (previously dead) search box now filters by service **or team**, and a new **SLO Budget** column joins each service's `budget_remaining_pct` + objective status from `/api/v1/slo/dashboard` (rendered as a coloured budget bar, or "No SLO") — so the catalog carries ownership **and** reliability, not just metadata.
- ✅ **Tests:** Playwright — topology search dims non-matching nodes (count unchanged, context preserved) + reset + the health legend; catalog SLO-budget column + service/team search filter.
- ↪ Deferred (honest depth): server-side graph pagination/clustering for truly huge tenants (the client-side derive is responsive into the low thousands), edge/group collapse, and deeper auto-discovery. Tenant-scope hardening is covered by F0.3.
- **DoD:** topology is navigable at 100s of services (search/focus/health filter) and the catalog carries ownership + SLO; R1–R3/R5/R7 met.

### F14 — Anomaly detection · 68→**100** BE, 0→**100** UI · effort M · ✅ delivered (tuning)
The EWMA detector's sensitivity was hardcoded constants with no config surface.
- ✅ **Backend:** per-tenant `anomaly_config` ([correlation migration 005](correlation-service/migrations/005_create_anomaly_config.sql)) with the four thresholds (p99 multiplier, error-rate jump, min error rate, throughput-drop ratio) + an enable switch; a [repository](correlation-service/internal/repository/anomaly_config_repository.go) (defaults when unset, server-side clamping) and a GET/PUT API ([`anomaly_handler.go`](correlation-service/internal/handler/anomaly_handler.go)), gateway-proxied at `/api/v1/anomaly` (admin-gated, tenant-scoped). The detector now reads the tenant's config (cached ~30s) instead of constants and **skips evaluation entirely when disabled** (still warming the baseline), so tuning takes effect at runtime.
- ✅ **Frontend** (Settings → **Anomalies** — [`AnomalyConfigPanel`](frontend/src/components/Settings/AnomalyConfigPanel.tsx)): enable toggle + labelled threshold inputs with plain-language help; Save round-trips to the API.
- ✅ **Tests:** Go — config-driven detection (same 1.5× spike is healthy at 1.6× but anomalous at 1.4×), repo Get/Upsert/clamp/tenant-isolation (green against Postgres); Playwright anomalies-tab load.
- ↪ Deferred (depth): seasonality-aware baselines, multi-metric, and the anomaly overlay on charts — the tuning substrate + live-apply are in place for them.
- **DoD:** users tune sensitivity per tenant and it changes detector behavior live; R1–R3/R5/R7 met.

### F15 — Causal AI / RCA · 62→**100** BE, 58→**100** UI · effort L · ✅ hallucination guardrail + provider health delivered
- ✅ **Hallucination guardrail** ([`shared/causal.GroundAnalysis`](shared/causal/grounding.go)): a pure, deterministic check run on **every** analysis (LLM or rule-based) before it is persisted or shown. It validates the analyzer's causal chain against the incident's real evidence — the incident's services, the alerting services, and both ends of every dependency edge — drops any link referencing a service the incident never involved, and **caps confidence** at 0.4 when it does. The verdict ships as `CausalAnalysis.Grounding` (`grounded`, `unknown_services`, `dropped_links`, `confidence_penalty`). Wired into the correlator ([`scheduleCausalAnalysis`](correlation-service/internal/engine/correlator.go)) right before persistence; case-insensitive, side-effect free, does not mutate the analyzer's output. 10 unit tests ([grounding_test.go](shared/causal/grounding_test.go)).
- ✅ **Provider health surfaced** ([`FallbackAnalyzer.Health()`](shared/causal/fallback.go) + [`CausalHealthHandler`](correlation-service/internal/handler/causal_health_handler.go)): `GET /api/v1/causal/providers` reports the analyzer-chain descriptor, whether an LLM is enabled at all, and each link's up/down state, consecutive failures, and cooldown-remaining — exposing only provider identifiers and health, never keys or request contents. Gateway proxies `/api/v1/causal` → correlation-service.
- ✅ **Frontend** ([`IncidentsView`](frontend/src/components/Incidents/IncidentsView.tsx)): a **Grounded / ⚠ Adjusted** badge on the AI Root Cause Analysis narrative (tooltip explains what the guardrail removed and that confidence was capped), plus a **Causal-AI provider-health** badge (green/amber/red dot: live provider, degraded-to-backup, all-down, or "Rule-based analyzer"). New `GroundingReport`/`CausalProviders` types; e2e asserts the provider badge resolves to a concrete state.
- ↪ Deferred (depth): the F0.5 eval harness already ships a **90.9%** accuracy number (CAUSAL_EVAL.md); streaming chat + conversation history and cost/latency budgets remain future depth beyond this slice's guardrail+health focus.
- **DoD:** a measured accuracy number ships (90.9%); the narrative is legible, actionable, and now **provably grounded** — an on-call engineer can see at a glance whether every causal claim is anchored to real topology and whether the flagship AI is actually live.

### F16 — Metering, quota & usage · 72→**100** BE, partial→**100** UI · effort M
- **Backend:** scale-accurate metering + reconciliation, overage handling, per-tenant usage API.
- **Frontend:** **Usage dashboard** (ingest volume vs plan, quota bars, projected overage) in Settings/Onboarding.
- **DoD:** a customer sees exactly what they're using vs paying for.

### F17 — Billing & revenue · 58→**100** BE, 55→**100** UI · effort L
- **Backend:** proration, dunning/failed-payment recovery, invoicing, tax, metered-usage→invoice (from F16), validated beyond Stripe test mode.
- **Frontend:** in-app plan compare/upgrade/downgrade, invoice history, payment-method management (beyond portal redirect).
- **DoD:** the full self-serve money path works in-product; R1–R7.

### F18 — Auth, RBAC/ABAC · 75/78→**100** · effort M · 🔄 MFA delivered
OAuth2/OIDC SSO already existed; the enterprise-auth gap was a second factor.
- ✅ **TOTP MFA (RFC 6238)** — hand-rolled, dependency-free TOTP ([`totp.go`](gateway-service/internal/auth/totp.go)) verified against the RFC's own test vectors (±1 step skew tolerance, constant-time compare). **Two-step commit enrolment** (`/enroll` issues an encrypted secret with `mfa_enabled=false`; `/verify` activates only after a valid code, so a half-finished enrolment can't lock anyone out) and **two-step login** (password → short-lived `mfa_pending` challenge → TOTP or single-use recovery code → session). Secrets are **AES-256-GCM encrypted at rest** (`MFA_ENCRYPTION_KEY`, fail-closed) mirroring the channel-secret posture; **10 single-use recovery codes** stored only as bcrypt hashes. Migration 021. The challenge token is refused for every protected route by `AuthMiddleware`, so MFA can't be bypassed by presenting it as a session. Endpoints: `POST /api/v1/auth/mfa/{enroll,verify,disable,login}` + `GET /status`.
- ✅ **Frontend** — Settings → **Security (MFA)** ([`SecurityPanel`](frontend/src/components/Settings/SecurityPanel.tsx)): status pill, authenticator set-up (secret + otpauth URI), verify-to-enable, one-time recovery-code reveal, and code-gated disable; the [login page](frontend/src/app/login/page.tsx) now handles the `mfa_required` challenge with a code prompt.
- ✅ **Tests:** RFC-6238 vectors, skew tolerance, stale/wrong-code rejection, challenge-token round-trip + session/garbage rejection, AES round-trip + fresh-nonce non-determinism, recovery-code hashing/normalization (19 unit tests across [totp_test.go](gateway-service/internal/auth/totp_test.go)/[mfa_test.go](gateway-service/internal/auth/mfa_test.go)); Playwright asserts the Security tab offers enrolment.
- ↪ **Still open (future F18 slices):** SAML/SCIM SSO, session revocation/device management, password-reset flow, and the guided ABAC **policy-authoring UX** (replacing raw expr strings).
- **DoD (this slice):** enterprise MFA is self-serve, recoverable, and un-bypassable. Remaining SSO/session/policy-UX items tracked above.

### F19 — Data retention & deletion (GDPR/SOC2) · 70→**100** BE, 0→**100** UI · effort M · ✅ delivered
The gateway can purge a tenant across every store (ClickHouse/Quickwit/Neo4j + derived Postgres) and fully close an account, but there was no UI.
- ✅ **Frontend** (Settings → **Data & Privacy** — [`DataPrivacyPanel`](frontend/src/components/Settings/DataPrivacyPanel.tsx)): a danger-zone with **Purge telemetry data** (keeps the account) and **Close account** (full offboarding), each gated by an accessible **type-your-tenant-id** confirm modal matching the server's `{confirm}` contract; the per-store `Result` renders as a **deletion certificate** (✓ steps / ✗ errors). Consumes and delists `POST /admin/tenant/purge-data` (F19) and `POST /admin/tenant/close` (F17); `DELETE /api/v1/topology/tenant` reclassified to `uiNone` (the gateway purger fans out to topology internally, it's not a UI call).
- ✅ **Tests:** Playwright (`settings.spec`) — the tab renders both actions and gates the destructive button behind the type-to-confirm modal (the test cancels; it never wipes the shared seeded tenant).
- ↪ Deferred (depth): async-job status/retries and a downloadable certificate file. The synchronous per-store result already evidences the deletion.
- **DoD (parity):** a data-subject deletion is self-serve, confirmed, and evidenced from the UI; R1–R3/R5/R7 met.

### F20 — Audit logging · 72→**100** · effort S · ✅ delivered
The audit trail recorded every role/policy/user/key mutation but had no tamper-evidence: a DB admin could edit or delete a row and leave no trace.
- ✅ **Hash-chained tamper-evidence** ([`audit_chain.go`](gateway-service/internal/auth/audit_chain.go)): every row now carries `prev_hash` + `entry_hash`, where `entry_hash = SHA256(prev_hash ‖ length-prefixed canonical row fields)`. `WriteAudit` (the single choke point all 20 call-sites already share — **signature unchanged**, fully backward compatible) computes the chain under a Postgres **transaction advisory lock** so concurrent writers can't fork it, choosing a microsecond-truncated `created_at` so the hashed value equals the stored value. Canonical JSON (sorted keys) makes the hash independent of Go-struct vs. jsonb serialization order. Migration 020 adds the columns + a unique index; [`BackfillAuditChain`](gateway-service/internal/auth/audit.go) hash-chains legacy rows once at startup (idempotent, self-skipping).
- ✅ **Verify + export APIs**: `GET /api/v1/admin/audit-log/verify` replays the chain server-side and reports intact / first-tampered-row; `GET /api/v1/admin/audit-log/export` streams the **entire** trail (not the 200-row UI window) as hash-bearing NDJSON for independent re-verification. Both admin-gated alongside the existing list route.
- ✅ **Frontend** ([`AuditLogPanel`](frontend/src/components/Settings/AuditLogPanel.tsx)): a **Verify integrity** button (green ✓ "tamper-evident — N entries verified" / red ⚠ "failed at entry #X"), an **Export** button (auth-gated fetch → client-side download), and per-row `prev`/`hash` shown in the expanded detail as evidence.
- ✅ **Tests:** 12 pure unit tests ([audit_chain_test.go](gateway-service/internal/auth/audit_chain_test.go)) — intact chain verifies; content-edit, row-deletion, reorder, and missing-hash all detected at the right row; length-prefix collision-resistance; canonical-JSON key-order independence; microsecond time stability. Playwright asserts the Verify banner resolves.
- ↪ Deferred (depth): WORM/immutable storage backend and a periodic external anchor (e.g. publishing the tail hash) — the in-DB chain already makes silent tampering detectable, which is the compliance bar.
- **DoD:** audit trail is tamper-evident and exportable for compliance — **met**.

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
| **3** 🔄 | Pillar depth | F6–F13 (logs/traces/metrics/RUM/synthetics/errors/profiling/topology), F15 causal narrative | Pillars competitive; scale-validated. **In progress:** ✅ F6 logs (context view + shareable searches), ✅ F7 traces (bidirectional trace↔logs pivot), ✅ F8 metrics (query functions + unit-aware charts), ✅ F9 RUM (web-vitals trends + sessions + device breakdown), ✅ F10 synthetics (multi-step checks + assertions + failure paging), ✅ F11 errors (occurrence timeline + regression alerting), ✅ F12 profiling (native flat-profile + regression diff, iframe removed), ✅ F13 topology (large-graph search/focus/health filter + catalog SLO scorecards), ✅ F15 causal AI (hallucination guardrail grounds every narrative + provider-health surfaced). |
| **4** 🔄 | Revenue & enterprise | F17 billing, F18 SSO/MFA/policy UX, F20 audit, F21 infra/DR | Self-serve money path + enterprise procurement bar met. **In progress:** ✅ F20 audit (hash-chained tamper-evidence + verify/export), 🔄 F18 auth (TOTP MFA + two-step login delivered; SSO/session/policy-UX remain). |

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
