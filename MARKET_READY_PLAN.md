# PulseTrace: Architecture Plan to Market-Ready

## Context

PulseTrace is a Go-microservices observability platform (gateway, log, alert, correlation, topology, notification, action services + Next.js frontend) with genuinely real infrastructure underneath the demo: JWT+bcrypt auth, DB-backed RBAC/ABAC, audit logging, OAuth/SSO, Kafka→ClickHouse log pipeline, alert→correlation→notification incident flow, Neo4j topology, real EWMA-based anomaly detection, and OTel self-instrumentation. Recent commits (`eef6592` "remove trust-blocking fakes and hardcoded secrets", `9c5507f`, `3db420c`, `dfa4405`) show a deliberate push from demoware to production code — CSRF state is now random, JWT secrets no longer fall back to a hardcoded string, migrations auto-apply, e2e coverage was added.

What's missing is the layer between "working distributed system" and "sellable product": tenant trust boundaries that hold up under a paying customer's security review, revenue mechanics (there are none), infra that survives real traffic, and per-pillar feature depth that competes with Datadog rather than demonstrates the concept. The user wants depth in *every* area and wants to keep both SaaS and enterprise-on-prem deployment models open, executing solo across iterative sessions — so this plan is phased into independently shippable increments, ordered by what blocks a real customer (trust/security) before what blocks revenue (billing) before what blocks competitiveness (feature depth) before what blocks scale (infra).

## Model & thinking-level guidance

- **Architecture/design work (this kind of task, RFCs, data-model changes, cross-service contracts):** Claude Opus 4.8, high or xhigh thinking. Multi-service reasoning (Kafka contracts, tenant isolation across 6 services, schema migrations) benefits from Opus's stronger multi-step planning, and the cost is incurred rarely.
- **Day-to-day implementation (writing the Go handlers, React components, migrations, tests once a plan exists):** Claude Sonnet 5, medium thinking. It's the current model in this session, is materially cheaper per token, and is plenty for well-scoped, plan-guided edits — reserve Opus for when a session gets stuck or the design itself is in question.
- **Bulk/mechanical work (adding the same pattern across 8 services, writing repetitive test fixtures, seed data):** Haiku 4.5 or Sonnet 5 with low thinking, dispatched via background subagents so they don't block your main thread.
- **Rule of thumb:** bump thinking level (not model) first when a task feels hard; bump model to Opus only when the task is genuinely cross-cutting/architectural, not just long.

## Phase 0 — Trust boundary fixes (do first, small blast radius)

These are the items that fail a security review immediately and are cheap to fix now before more code is built on top of the current gaps.

1. **Tenant spoofing on ingestion.** `gateway-service/internal/auth/auth.go:164-176` — unauthenticated log/trace/metric/RUM ingestion endpoints trust a client-supplied `X-Tenant-ID` header verbatim. Any caller can write data into any other tenant's `tenant_id`. Fix: issue per-tenant ingestion API keys (new `ingestion_keys` table: `key_hash`, `tenant_id`, `tier`, `revoked_at`), require `Authorization: Bearer <ingest-key>` on these routes, resolve `tenant_id`/`tier` server-side from the key — never from a client header. This is the single highest-leverage fix for both SaaS and enterprise trust.
2. **Secrets in git.** `k8s/secret.yaml` is checked in with placeholder values but real deployments will edit it in place, which then risks a real secret landing in git history. Move to `.gitignore` + a `secret.yaml.example` template, and document (in `k8s/README.md` or similar) that production deploys use Sealed Secrets, External Secrets Operator, or Vault — pick one and wire an example manifest, don't just leave the comment.
3. **`action-service/internal/k8s/operator.go`** hardcoded `namespace: "default"` — any remediation action (pod restart, scale) only works if the target service happens to live in `default`. Needs to resolve namespace per-service from the topology/service-catalog record, not a constant. This blocks self-healing runbooks from working in any real multi-namespace cluster — both SaaS (multi-tenant namespaces) and enterprise (customer's own namespace conventions) need this.
4. **CI doesn't run the Playwright e2e suite** (`scripts/run-e2e.sh` exists but only runs locally; `.github/workflows/ci.yml` stops at frontend build/lint). Wire it into CI (it already knows how to seed data and wait for health) so regressions in "every page and flow" are caught before merge, not after.

## Phase 1 — Multi-tenant data isolation (foundational, blocks both GTM paths)

`tenant_id` columns exist but isolation appears to be enforced ad hoc per-query rather than structurally guaranteed.

- Audit every read path in `gateway-service`, `correlation-service`, `log-service` (ClickHouse queries), and `topology-service` (Neo4j queries) for a `WHERE tenant_id = ?` / equivalent Cypher filter. Any endpoint missing this is a cross-tenant data leak.
- Add a shared query-builder or middleware in `shared/` that injects tenant scoping automatically (e.g., a `TenantScopedDB` wrapper) so new endpoints can't forget it — this is the kind of cross-cutting contract change worth doing with Opus/high-thinking, since it touches every service.
- ClickHouse: consider row-level tenant partitioning (partition key includes `tenant_id`) both for isolation and for cheap per-tenant data deletion (needed for Phase 2 compliance anyway).
- Neo4j: topology nodes need tenant scoping too, or one tenant's service map can leak into another's causal analysis.

## Phase 2 — Monetization & SaaS operability

Currently zero revenue mechanics exist despite a `tier` column already threaded through auth/JWT/ABAC.

- **Usage metering:** emit per-tenant ingestion volume (logs/traces/metrics/RUM events) as a Kafka topic or periodic aggregation job, stored in Postgres (`usage_daily` table) — this is the substrate both billing and quota enforcement need.
- **Quota enforcement:** extend the existing ABAC policy engine (`gateway-service/internal/auth`, `abac_policies` table) with tier-based rate/volume limits rather than building a parallel system — it already evaluates conditions per-tenant.
- **Billing:** Stripe (or similar) subscription + metered billing integration, webhook handler analogous to the existing `github_webhook.go` pattern, mapping Stripe events → tier changes on the `users`/tenant record.
- **Self-serve onboarding:** the `Register` handler already exists (`auth.go:409`) defaulting new users to `viewer`/`standard` tier — extend it into a real signup flow (tenant creation, not just user creation; email verification; initial admin role instead of viewer for the first user in a new tenant).
- **Data retention & deletion:** GDPR/SOC2-style "delete my data" needs a real implementation given logs/traces/RUM commonly contain PII — tie this to the ClickHouse tenant-partitioning work from Phase 1.

## Phase 3 — Feature depth per observability pillar

Bring each pillar from "present" to "competitive." Prioritize based on what differentiates PulseTrace per `SYSTEM_ARCHITECTURE.md`'s own pitch (causal AI, self-healing) since that's the actual wedge — commodity pillars (logs/metrics) need to be solid but not gold-plated first.

- **Causal AI / RCA:** `shared/causal/langchain.go`, `correlation-service/internal/llm` — verify depth of the LLM router (does it actually fall back cleanly between Anthropic/OpenAI/local Ollama, or is only one path real?). This is the flagship feature; it needs to work end-to-end with test coverage, not just exist.
- **Self-healing runbooks:** `action-service` — beyond the Phase 0 namespace fix, verify what remediation actions actually execute (restart pod, scale, rollback) vs. what's aspirational in the docs. Add a dry-run/approval mode (enterprise buyers will not want fully automatic prod remediation without a human-in-the-loop toggle).
- **Anomaly detection:** already real (EWMA-based, `correlation-service/internal/engine/anomaly_detector.go`) — extend beyond single-metric (p99 latency) baselines to error-rate and throughput anomalies using the same pattern.
- **Alerting integrations:** only Slack + SMTP are real; `SYSTEM_ARCHITECTURE.md` claims PagerDuty. Add PagerDuty/Opsgenie/generic webhook to `notification-service/internal/worker/notification_worker.go` following the existing Slack/SMTP pattern.
- **Log/trace query depth:** verify saved searches, full-text/regex search, and query performance at realistic cardinality — these are table-stakes for any observability buyer and easy to under-invest in relative to flashier AI features.
- **RUM & synthetics:** confirm these are real implementations and not stubs (the explore pass didn't deeply verify); if partial, this is lower priority than causal AI/self-healing per the differentiation argument above.

## Phase 4 — Production infra & scale

- **Secrets management:** resolve per Phase 0 item 2 (Vault/Sealed Secrets/External Secrets), for both SaaS control-plane and enterprise on-prem deploys — pick a design that works for both (e.g., External Secrets Operator syncing from whichever backend the deployer chooses).
- **K8s hardening:** add HPA (especially for `gateway-service` and `log-service`, the ingestion-path services), PodDisruptionBudgets, resource requests/limits if missing, and package the existing raw `k8s/` manifests as a Helm chart with values for both SaaS (single shared cluster) and enterprise (customer-controlled namespace/values) deployment shapes.
- **CI/CD:** extend `.github/workflows/ci.yml` to run e2e (Phase 0), add a staging deploy step, and add basic load testing (k6 or similar) against the ingestion path — Kafka/ClickHouse throughput under load is the thing most likely to fall over first with real customer traffic.
- **Multi-region/DR:** lower priority than the above; revisit once there's a first real customer needing it, don't build speculative multi-region infra now (matches "no future-proofing for hypothetical requirements").

## Suggested sequencing

Phase 0 → Phase 1 → Phase 2 and Phase 3 can run concurrently (they touch mostly disjoint code — billing/metering vs. per-pillar feature work) → Phase 4 last, since infra scaling work is only worth doing once the product surface it's scaling is closer to final.

## Verification per phase

- Phase 0/1: write a test that attempts cross-tenant read/write (using tenant A's ingest key or JWT to access tenant B's data) and confirm it's rejected — add this to the Go test suite and to CI.
- Phase 2: exercise the full signup → tier assignment → quota breach → Stripe webhook → tier change loop manually against the local docker-compose stack, then add integration tests.
- Phase 3: for causal AI, run an actual failure scenario through the docker-compose stack (kill a dependency, confirm alert→correlation→causal narrative→notification fires correctly) — this pipeline was recently fixed (`9c5507f`) so regression coverage here matters most.
- Phase 4: load-test the ingestion path in a scratch environment before rolling Helm/HPA changes into any shared environment.
