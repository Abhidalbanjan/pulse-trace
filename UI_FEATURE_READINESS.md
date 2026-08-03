# PulseTrace — UI Feature Readiness (screen-by-screen)

_Last updated: 2026-08-03_

This walks the product **as a user sees it** — every item in the left nav plus the
cross-cutting surfaces — and scores each screen 0–100% on UI/UX market readiness:
is it wired to real data, interactive, complete on loading/error/empty states, and
deep enough to compete?

> **This is a different lens from [FEATURE_READINESS.md](FEATURE_READINESS.md).**
> That file scored **backend capability**; this scores the **screen**. They diverge
> where a strong backend has a thin or missing UI — most starkly for **self-healing
> approvals** (backend ~60%, but there is no approve/reject screen) and **SLOs**
> (real engine, no view at all).

### Method
Read from `frontend/src/` on 2026-08-03: which endpoints each screen calls, whether
it mutates or only displays, and whether it handles loading/error/empty. Not a
usability test — no real users were observed.

---

## The 15 nav screens

| # | Screen (nav label) | Confidence | Wired? | What's missing to raise it |
| ---: | --- | ---: | :---: | --- |
| 1 | **AI SRE** (home — causal chat + action cards) | **58%** | ✅ `/api/v1/chat` | Real NL assistant that can surface a remediation "action card". But executing it pops a placeholder `alert()` rather than a proper confirm→run→result flow; no streaming, no conversation history, answer quality unmeasured. |
| 2 | **Incidents** | **55%** | ✅ `/api/v1/incidents` | Lists incidents + renders the causal chain and a detail pane. **No approve / reject / dry-run controls** — the human-in-the-loop remediation the backend fully supports has no UI here. Also missing: timeline view, filtering/search, status transitions. |
| 3 | **Deploy Gates** (Deployments) | **25%** | ❌ **no fetch** | A shell that only ever renders its empty state ("…will appear here"). No data is fetched; the shift-left gate UI is a placeholder. |
| 4 | **Onboarding** | **65%** | ✅ signup, keys, billing checkout | Real wizard: signup → tenant → ingestion-key reveal → Stripe checkout. Missing: email-verification polish, plan comparison, "install the agent" verification step, resumability. |
| 5 | **Log Explorer** | **78%** | ✅ search, saved-searches | One of the strongest screens: full-text/regex search, time-range, saved searches, facet charts, solid loading/error/empty states. Missing: scale/cardinality proof, field-level histograms, live tail. |
| 6 | **Distributed Traces** | **78%** | ✅ analytics + facets | The most built-out screen (~960 lines): trace search, facets, waterfall/analytics, good empty/error handling. Missing: span-level deep-link UX, trace→logs correlation polish, exemplar linking. |
| 7 | **Services** (+ service detail) | **75%** | ✅ services, RED metrics, deps | Rich: RED metrics, dependency data, charts, polling. Missing: consistent SLO surfacing, ownership metadata, saved views. |
| 8 | **Metrics** | **68%** | ✅ `/api/v1/metrics/query` | Real charts with query + polling. Missing: PromQL-grade query UX, multi-series/dashboard building, alert-from-chart, unit handling. |
| 9 | **Error Tracking** | **64%** | ✅ error groups + resolve | Real fingerprint groups with resolve + polling. Missing: source-map de-minification, occurrence detail depth, assignment/ownership, release regression view. |
| 10 | **Continuous Profiler** | **55%** | ✅ profiler + comparison-diff | Real controls (type/service/time, compare mode) and a diff endpoint. Missing: in-product flamegraph depth (leans on Pyroscope), loading/empty states, regression callouts. |
| 11 | **Real User Monitoring** | **62%** | ✅ analytics + errors | Web-vitals number cards + recent JS errors, loading/empty states. Missing: **trend charts** (values are point-in-time cards, no history), session detail, geo/device breakdowns, sampling controls. |
| 12 | **Synthetic Monitoring** | **66%** | ✅ tests CRUD + results | Create/list tests, view results. Missing: multi-step/browser checks, richer assertions, per-test failure→alert wiring, latency-trend charts. |
| 13 | **Topology** | **70%** | ✅ `/api/v1/topology/graph` | Real Neo4j-backed dependency graph with error/empty states. Missing: interaction depth (filter/focus/collapse), scale behaviour on large graphs, health overlay polish. |
| 14 | **Catalog** | **66%** | ✅ `/api/v1/topology/catalog` | Real service catalog listing. Missing: ownership/metadata richness, search/filter, per-service scorecards. |
| 15 | **Settings** (admin) | **80%** | ✅ 8 admin APIs | The most complete surface: Roles, Policies, Rate-limits, Users, Alert-rules, Audit-log, Billing panels — all with real CRUD and heavy error handling. Missing: **ingestion-key _rotation_ is API-only** (the new rotate endpoint has no button yet — list/create/revoke exist), policy-authoring guidance, MFA settings. |

## Auth & entry

| Screen | Confidence | What's missing to raise it |
| --- | ---: | --- |
| **Login / Signup / SSO** | **75%** | Real login, signup, and SSO login/callback. Missing: MFA, password-reset flow, SSO provider setup UX, "remember device". |

---

## Cross-cutting UI gaps (features with a backend but little/no screen)

| Capability | Backend | UI today | UI score |
| --- | :---: | --- | ---: |
| **Self-healing approvals** (approve/reject/dry-run, risk-tier authz) | ✅ real | **No dedicated screen** — only reachable as an AI-SRE chat "action card" that `alert()`s. The Incidents screen shows the plan but can't action it. | **~20%** |
| **SLO / error-budget / burn-rate** | ✅ real engine | **No view at all** (`slo` exists only as an RBAC permission string). | **~10%** |
| **Alerting channel config** (Slack/PagerDuty/Opsgenie/webhook) | ✅ real + auto-resolve | Configured by **env vars only**; no UI to add/test a channel. Alert _rules_ have a panel; delivery _channels_ don't. | **~15%** |
| **Ingestion-key rotation** | ✅ real (new) | List/create/revoke are in Settings; **rotate has no button yet**. | **~40%** |
| **Billing management** | ✅ Stripe | Checkout + portal redirect exist (Onboarding + Billing panel); no in-app invoices/usage-vs-plan/upgrade UX. | **~55%** |

## General UI-quality observations (affect every screen)

- **States are mostly handled** — loading/error/empty appear across the built screens (Traces, Services, Settings especially), which is better than typical demoware.
- **Type safety is loose** — heavy `useState<any[]>` and ad-hoc mapping; refactoring risk, not a user-visible bug.
- **Actions sometimes stub out** — `alert(...)` stands in for a real result flow in the AI-SRE execute path.
- **No design-system depth** — inline styles + a theme object; consistent enough, but no component library, a11y, keyboard nav, or responsive/mobile story validated.
- **No real-time depth** — polling in a few places; no live tail / streaming updates.

---

## Roll-up (UI lens)

| Tier | Screens |
| --- | --- |
| **Strong (75–80%)** | Log Explorer, Distributed Traces, Services, Settings, Login |
| **Solid, gaps (62–70%)** | Topology, Metrics, Onboarding, Synthetics, Catalog, RUM, Error Tracking |
| **Thin (55–58%)** | AI SRE (chat), Incidents, Profiler |
| **Placeholder / absent** | Deploy Gates (25%), self-healing approvals (~20%), alert-channel config (~15%), SLO view (~10%) |

**Blended UI readiness: ~60%** — a touch below the ~66% backend blend, and for a
clear reason: several genuinely-built backend capabilities (self-healing approvals,
SLOs, alert-channel config, key rotation) are **under-exposed or invisible in the
UI**. The observability pillars (logs/traces/services/settings) are the mature
screens; the differentiators are where the UI lags the backend most.

### Highest-leverage UI work (score-per-effort)

1. **Build the remediation approve/reject/dry-run panel on Incidents** — the backend is done; this is the flagship's missing face (Incidents 55 → 75, self-healing UI 20 → 70).
2. **Wire Deploy Gates to real data or hide it** — a nav item that only shows an empty state reads as broken (25 → 70, or remove).
3. **Add an SLO / error-budget screen** — the engine and data exist (0 → 65).
4. **Add an alerting-channels settings panel** (add/test Slack/PD/Opsgenie) — env-only config is an ops smell for a paid product (15 → 70).
5. **Replace `alert()` execution in AI SRE with a real confirm→run→result flow** (AI SRE 58 → 72), and add the rotate button to the ingestion-keys panel.

_Scores reflect frontend maturity read from `frontend/src/` on 2026-08-03; they are a prioritisation aid, not a usability study._
