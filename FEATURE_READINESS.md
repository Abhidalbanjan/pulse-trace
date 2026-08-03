# PulseTrace — Feature Market-Readiness Assessment

_Last updated: 2026-08-03_

This is an **honest, code-grounded** self-assessment of every feature PulseTrace
currently supports, scored 0–100% on market readiness (i.e. "would this survive a
paying customer's daily use and a security/procurement review?"). Where the score
is below ~85%, the concrete gap is named.

> **What the score is _not_:** it is a judgement of implementation maturity from
> the code, tests, and local verification — **not** a substitute for a third-party
> security audit, a SOC 2 report, or a real customer pilot at production scale. The
> single biggest cross-cutting unknown is **behaviour under sustained real load**
> (see Infra → _Load/scale validation_): most pillars are functionally complete but
> have only been exercised at demo volume.

### Scoring bands

| Band | Meaning |
| --- | --- |
| **90–100** | Production-hardened: real, tested, verified, no known material gap. |
| **75–89** | Solid and real; minor gaps (edge cases, UX polish, scale unproven). |
| **60–74** | Works end-to-end; notable gaps a paying customer would hit (coverage, depth, external-config dependence). |
| **40–59** | Real implementation, meaningful missing pieces or unproven at scale. |
| **< 40** | Partial, stubbed, or deferred. |

---

## 1. Ingestion & Multi-Tenancy (the trust foundation)

| Feature | Confidence | What's missing to raise it |
| --- | ---: | --- |
| In-process OTLP termination + tenant stamping (gRPC + HTTP) | **88%** | Hardened, tested, verified; tenant.id stamped server-side. Missing: TLS/mTLS is optional and off by default; no load validation of the forward path. |
| Per-tenant ingestion-key auth (SHA-256, cache, scopes) | **86%** | Real, cached, RUM-scope carve-out enforced. Missing: no key-usage analytics/anomaly detection, no per-key rate limits. |
| **Ingestion-key rotation w/ grace window** | **84%** | Just shipped (RUM-safe grace, `replaced_by` lineage), verified against live Postgres. Missing: no automatic-rotation scheduler; admin UI is API-only. |
| Multi-tenant data isolation (Postgres/ClickHouse/Neo4j/Quickwit) | **80%** | `tenant_id` threaded everywhere; live cross-tenant read/write test in CI. Missing: isolation is enforced per-query, not _structurally_ (no row-level security / forced query builder); one forgotten `WHERE` is a leak. |
| Datadog/Splunk "zero-code" migration ingestion | **80%** | Full trace/metric/log translation, tenant-stamped, smoke-verified; decoder hardened against decode-bomb OOM + v0.5 arity drift. Missing: broad real-agent compatibility matrix untested; no throughput validation of this path. |

## 2. Observability Pillars

| Feature | Confidence | What's missing to raise it |
| --- | ---: | --- |
| **Logs** — ingest → Kafka → Quickwit → explorer (regex, time-range, saved searches) | **78%** | All log sources (app, migration, OTLP-native) now unified into one queryable index. Missing: cardinality/scale behaviour unproven; Quickwit operational maturity (retention, reindex, backpressure) not battle-tested. |
| **Traces** — OTLP → ClickHouse + Jaeger + Quickwit, tail sampling, analytics | **75%** | Real multi-sink pipeline, p99 analytics, saved views. Missing: trace-analytics depth (service maps from traces, span-level search UX), scale validation, exemplar linking. |
| **Metrics** — OTLP → ClickHouse, Prometheus scrape, metrics UI | **66%** | Native pillar with a real UI reader. Missing: per-service `/metrics` handlers not all wired (self-metrics partly placeholder); no PromQL-equivalent query UX; limited dashboarding. |
| **RUM** — web vitals + errors + page views → ClickHouse, public tokens | **70%** | Real browser ingest, p75 web vitals, tenant-scoped public tokens with rotation. Missing: session replay, sampling controls, richer dashboards, a hardened/versioned browser SDK. |
| **Synthetics** — Postgres state + Go probe worker (SSRF-hardened) | **68%** | Real scheduled worker, tenant-attributed results, target CRUD. Missing: single-region probing only, HTTP-only (no multi-step/browser checks), limited assertion types, no alerting wired to failures. |
| **Error tracking** — fingerprint grouping, resolve workflow | **62%** | Real grouping + resolve/mute lifecycle. Missing: source-map de-minification, release/regression tracking, dedup sophistication, alert routing on new groups. |
| **Continuous profiling** — Pyroscope integration | **55%** | Wired and collecting. Missing: thin product surface (mostly passthrough to Pyroscope UI), no flamegraph diffing/regression detection, unproven overhead controls. |
| **Service catalog & topology** — Neo4j graph | **65%** | Real dependency graph, tenant-scoped, drives RCA. Missing: auto-discovery depth, ownership/metadata richness, graph scale validation. |

## 3. Differentiators (the wedge: causal AI + self-healing)

| Feature | Confidence | What's missing to raise it |
| --- | ---: | --- |
| **Causal AI / RCA** — multi-provider LLM chain + deterministic fallback | **62%** | Real fallback (Anthropic → Ollama → rule-based), never hard-fails, runs end-to-end. Missing: **no evaluation harness** — narrative _quality_/accuracy is unmeasured; hallucination guardrails thin; LLM cost/latency controls basic; requires an API key to be more than rule-based. This is the flagship claim and needs an eval story before it's sellable as "AI RCA." |
| **Self-healing remediation** — action-service (restart/scale/rollback), human-in-loop, dry-run, **per-action risk authz** | **60%** | Real client-go execution, approval workflow, dry-run, and (new) risk-tier approval authz. Missing: falls back to MOCK without a reachable cluster; only 3 action types; no post-remediation verification / auto-revert on failure; in-cluster RBAC for the operator is example-grade. |
| **Anomaly detection** — EWMA (latency, error-rate, throughput) | **68%** | Real, tested, multi-signal. Missing: no seasonality/holiday awareness, single-metric baselines, limited auto-tuning, no alert-fatigue controls. |
| **SLO & error-budget burn-rate alerting** | **66%** | Real burn-rate engine + budget tracking, tested. Missing: limited SLI sources, no multi-window multi-burn-rate presets UI, budget-policy depth. |
| **Alerting integrations** — Slack, SMTP, PagerDuty, Opsgenie, signed webhook + **auto-resolve** | **80%** | All real and tested; incidents now auto-resolve and auto-close PD/Opsgenie on recovery. Missing: no routing/escalation policies, on-call schedules, or per-severity channel mapping; dedup is incident-ID only. |

## 4. Platform, Auth & Revenue

| Feature | Confidence | What's missing to raise it |
| --- | ---: | --- |
| Auth — JWT + bcrypt, OAuth/SSO, register | **75%** | Solid core, no hardcoded-secret fallbacks, random CSRF state. Missing: MFA, SSO provider breadth (SAML/SCIM), session revocation/device management. |
| RBAC + ABAC — data-driven roles + expr-lang policies | **78%** | Genuinely strong: dynamic roles, resource-scoped permissions, priority-ordered deny-first policies, admin fallback. Missing: policy-authoring UI, more test coverage on ABAC edge cases. |
| Usage metering — per-tenant, all ingest paths | **72%** | Real per-signal metering into Postgres. Missing: accuracy reconciliation at scale, late-data handling, exposed per-tenant usage dashboards. |
| Quota enforcement — per-plan monthly limits | **72%** | Real, wired into every ingest path incl. migration + OTLP logs. Missing: soft-limit/grace UX, overage billing linkage, burst allowances. |
| **Billing** — Stripe (real HTTP) behind a provider interface + webhooks | **58%** | Real Stripe subscription calls + webhook → tier changes; provider-agnostic. Missing: proration, dunning/failed-payment recovery, invoicing/receipt UI, tax, metered-usage billing wiring, and validation beyond Stripe test mode. |
| Self-serve onboarding — tenant creation + signup funnel | **60%** | Real tenant+admin creation, onboarding wizard. Missing: email-verification robustness, trial/plan-selection flow, onboarding polish. |
| Data retention & deletion (GDPR-style per-tenant purge) | **70%** | Real cross-store purge (Postgres/ClickHouse/Quickwit/Neo4j). Missing: completeness auditing, async-job robustness/retries, deletion-certificate/verification, retention-policy UI. |
| Audit logging | **72%** | Real actor/action/resource trail on admin mutations. Missing: tamper-evidence (hash chaining), export, immutable retention. |

## 5. Infrastructure & Operations

| Feature | Confidence | What's missing to raise it |
| --- | ---: | --- |
| Kubernetes deployment — manifests, HPA, PDBs, Helm chart | **70%** | Real, `helm lint`/`template` clean, SaaS + enterprise values. Missing: not yet run in a real production cluster; resource envelopes unbenchmarked. |
| CI/CD — build, lint, Playwright e2e, k6 load gate | **65%** | E2E + DB-backed tests + load harness in CI. Missing: staging-deploy stage, container/dependency security scanning, enforced perf baselines. |
| Secrets management — External Secrets / Sealed Secrets examples | **55%** | Secrets out of git; example manifests provided; chart never creates the Secret. Missing: not wired to a concrete backend (Vault/cloud SM) end-to-end; operator must choose and integrate. |
| Self-observability — OTel self-instrumentation, Prometheus, Grafana | **70%** | Real dogfooding. Missing: curated SLO dashboards for the platform itself, alerting on platform health. |
| **Load / scale validation** (Kafka + ClickHouse under sustained load) | **40%** | k6 harness exists and runs in CI. Missing: **no validation at real customer volume** — the ingestion path's throughput ceiling, ClickHouse write amplification, and Kafka backpressure behaviour are unproven. This is the top cross-cutting risk. |
| Multi-region / DR | **15%** | Deliberately deferred (no speculative build). Missing: everything — replication, failover, regional data residency. Revisit at first customer that needs it. |

---

## Roll-up

| Category | Weighted readiness |
| --- | ---: |
| Ingestion & multi-tenancy | **~83%** |
| Observability pillars | **~68%** |
| Differentiators (causal AI / self-healing) | **~66%** |
| Platform, auth & revenue | **~69%** |
| Infrastructure & operations | **~52%** |
| **Overall (honest blended)** | **~66%** |

### The five things most worth doing next (biggest score-per-effort lift)

1. **Prove the ingestion path at scale** (Infra 40 → 70+). A real load test of Kafka→ClickHouse and the OTLP/migration front door is the single highest-leverage item; almost every pillar's score is capped by "scale unproven."
2. **Give causal AI an evaluation harness** (Differentiator 62 → 75+). The flagship claim needs measured accuracy, not just a working LLM call.
3. **Finish billing for real revenue** (58 → 75+): proration, dunning, invoicing UI, and metered-usage → invoice wiring.
4. **Structural tenant isolation** (80 → 90+): a forced tenant-scoping query layer so isolation can't be forgotten, plus ClickHouse row-level tenant partitioning.
5. **Self-healing depth + safety** (60 → 75+): post-action verification and auto-revert, more action types, real in-cluster RBAC.

_Scores reflect implementation maturity as read from the code and local verification on 2026-08-03. They are a starting point for prioritisation, not a compliance attestation._
