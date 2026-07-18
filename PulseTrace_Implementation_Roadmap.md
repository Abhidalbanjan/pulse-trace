# PulseTrace — Phased Implementation Roadmap (20% → Global-Ready)

*Companion to PulseTrace_Strategy_Report.md, prepared July 18, 2026*

Assumption: you're a solo founder or very small team (1–3 engineers). Durations below are effort-based sprints, not calendar dates — compress them if you add people, but don't skip the exit criteria to save time. Each phase ends with a gate: don't move to the next phase until you can honestly check the gate off, because every phase after Phase 0 assumes the product no longer lies to the person using it.

## Timeline at a glance

| Phase | Focus | Effort (small team) | Can run in parallel with |
|---|---|---|---|
| 0 | Trust & safety hardening | 2–3 weeks | — (do this first, alone) |
| 1 | MVP completeness | 4–8 weeks | Phase 3's AI-agent wedge can start here |
| 2 | Frictionless adoption | 4–6 weeks | Overlaps end of Phase 1 |
| 3 | Differentiation & moat | 6–10 weeks | Overlaps Phase 1/2 for the AI wedge |
| 4 | Enterprise & scale readiness | 8–12 weeks | Start once first serious enterprise conversation begins |
| 5 | Global launch & growth loop | Ongoing | Starts once Phase 2 gate is passed |

Total to a genuinely marketable, trustworthy full platform: roughly **6–9 months** solo, **3–5 months** with 3 focused engineers. Enterprise-grade (Phase 4) extends further and should be paced to actual deal pipeline, not built speculatively.

---

## Phase 0 — Trust & Safety Hardening
**"Stop the bleeding." Nothing ships to a prospect until this phase is done.**

Why first: every other phase's ROI depends on the product not embarrassing itself. A fast follow-up feature built on top of a fake anomaly detector is wasted work.

Tasks:
1. Replace `correlation-service/internal/engine/anomaly_detector.go`'s `rand.Float64()` placeholder with a real statistical detector — start simple: rolling z-score or EWMA baseline against real ingested metrics/RED stats. Ship "simple but real" before "impressive but fake."
2. Wire real notification delivery in `notification-service`: Slack incoming webhook, SMTP/SendGrid email, generic outbound webhook. PagerDuty/Opsgenie can wait for Phase 1 if needed, but Slack + email + webhook must work.
3. Fix `action-service`'s `ROLLBACK` — either implement it against `client-go` for real, or remove the button until it's real. Same rule for `frontend/src/app/page.tsx`'s fake-success catch block on failed actions.
4. Replace the hardcoded fake opening chat message with either nothing (empty state) or a real query against live data.
5. Remove all hardcoded secret fallbacks (`jwtSecret` default, ClickHouse credentials in ~6 files, static OAuth CSRF state) — require them from env/secret manager, fail loudly if missing in non-dev mode.
6. Add a one-page internal "no fake success" policy: any UI action that can fail must surface real failure. This is a design rule, not a single ticket — enforce it in code review for everything built after this phase.

**Exit gate:** you can run a live, unscripted demo — trigger a real Slack alert, watch a real anomaly get flagged from real data, click every action button — and nothing on screen claims something that didn't happen.

---

## Phase 1 — MVP Completeness
**"Make it actually a full-stack platform, not tracing-plus-scaffolding."**

Tasks:
1. **Native metrics pillar**: storage (you already have ClickHouse and Kafka in the stack — extend rather than add new infra), a query path, and dashboard UI inside the product. Stop leaning on bundled vanilla Grafana/Prometheus as the metrics story — right now that quietly breaks the "single pane of glass" pitch.
2. **User-defined alert rules**: threshold-based, composite/multi-condition, and anomaly-based (using the Phase 0 detector). Keep the existing SLO burn-rate engine as the flagship alert type, but it can't be the *only* type.
3. **Query experience**: given the LLM integration already exists, prioritize natural-language-to-query over building a new PromQL/LogQL clone from scratch — it's less work and more differentiated (see Phase 3).
4. **Testing & CI**: get real coverage on the RBAC engine, SLO worker, correlator, and repository layers; stand up GitHub Actions for build+test+lint on every PR. This isn't optional once you're asking anyone to trust this with their production data.
5. **Complete the Kubernetes path**: add the missing topology-service and action-service manifests, produce a Helm chart, so "self-host on your own k8s" is a real, tested option, not a partial one.
6. Close remaining stubs found in the audit (log-service's 501 search endpoints, etc.) — make the in-product log search path work end-to-end rather than depending entirely on Quickwit's own UI.

**Exit gate:** a small team could run PulseTrace as their *only* observability tool for a real production service — dashboards, metrics, logs, traces, and alerts that actually page someone — with CI green.

---

## Phase 2 — Frictionless Adoption
**"Make it trivial to try." This is where most challenger observability products win or lose the first 100 users.**

Tasks:
1. **Zero-instrumentation trial path**: build or integrate an eBPF-based collector mode (this is where the whole category is heading in 2026 — 67% of Kubernetes-scale teams already use at least one eBPF tool) so a prospect gets real traces/topology within minutes without touching their code. Keep OTel SDK instrumentation as the "go deeper" option, not the entry requirement.
2. **Radically simplified deploy**: a slimmed "core" docker-compose profile (or a hosted sandbox) that doesn't require standing up all 18 containers just to see value. Save the full stack for production self-host docs.
3. **Migration copilot**: parse existing Datadog/New Relic/Grafana dashboard and alert-rule exports and auto-translate them into PulseTrace equivalents, using the existing LLM layer to do the semantic mapping. This is the single highest-leverage GTM feature — it directly attacks the real reason people stay on expensive incumbents (migration cost), not just the price tag.
4. **Documentation & onboarding**: write the root README that doesn't currently exist, a real quickstart, and a short demo video/GIF. First impression currently is unedited `create-next-app` boilerplate in the frontend README — fix that too.
5. **Pricing page + billing**: implement transparent, predictable pricing (Stripe or similar) — this doubles as the marketing decision described in Phase 3/differentiation below, so design the pricing model deliberately, not as an afterthought.

**Exit gate:** a stranger with no help from you can go from signup to seeing their own service's live traces, topology, and one working alert in under 15 minutes.

---

## Phase 3 — Differentiation & Moat
**"Give people a reason to switch, not just a cheaper bill." Can start in parallel with Phase 1/2 for the AI-agent wedge specifically, since it needs the least dependency on the metrics/alerting work.**

Tasks:
1. **SLO-first alerting as the default onboarding path**, not an advanced/hidden option — you already have real SRE-grade burn-rate math built; most competitors gate this behind enterprise tiers. Make it the first thing a new user sets up.
2. **Conversational query across all signal types** — "why did checkout latency spike at 2am" answered by pulling logs, traces, topology, and SLO data into one narrative, with the underlying query shown for transparency. Natural extension of the LLM/causal-RCA work already in the repo.
3. **AI-agent / LLM observability module as a standalone beachhead product**: agent tool-call tracing, token/cost tracking, hallucination/quality signals. This category has no entrenched leader yet (~15% of GenAI deployments currently instrument observability at all) and lets you acquire customers who have zero existing observability investment to migrate away from — a much easier first sale than competing head-on in APM. Once they're in for their AI stack, expand them into full-stack.
4. **Expand real self-healing actions**, always reversible and logged — build on the genuinely-working `RESTART_PODS`/`SCALE` k8s actions rather than adding more surface area of things that only look automated.
5. **Make "radical honesty" a stated, marketed design principle** once it's true: every AI insight shows its evidence and confidence score, every automated action is reversible and logged, nothing reports fake success. Given the Phase 0 findings, this becomes a sharper story for a challenger brand than "we're cheaper."

**Exit gate:** you have one or two features that prospects or press call out unprompted as "I haven't seen that anywhere else" — not just "that's affordable."

---

## Phase 4 — Enterprise & Scale Readiness
**"Survive procurement." Pace this to actual pipeline — don't build it speculatively before you have real enterprise conversations happening.**

Tasks:
1. SOC 2 Type 1 roadmap — you have a real head start here since the RBAC/ABAC engine and audit logging are already mature; formalize policies and evidence collection around what's already built.
2. Data residency options — India (DPDP Act) and EU (GDPR) regions. This is a genuine structural advantage over US-headquartered incumbents for residency-sensitive buyers, not just narrative.
3. HA/scale hardening: sharding strategy, retention/downsampling policy, and — build for real this time — the multi-cloud cold storage tiering to S3/Azure/GCS currently described only in architecture docs.
4. SSO/SAML beyond the current Google SSO for enterprise identity providers (Okta, Azure AD, etc.), and remove the dev-mode mock fallback from the auth code path before anyone external touches it.
5. Status page, documented SLAs, and a written incident response process.
6. Load-test at realistic multi-tenant scale before quoting uptime numbers to anyone.

**Exit gate:** you can answer a mid-size company's security questionnaire without an asterisk on any "no."

---

## Phase 5 — Global Launch & Growth Loop
**Ongoing, starts once Phase 2's gate is passed — you don't need Phase 4 complete to start this, just to close larger deals.**

Tasks:
1. Design partner program: 5–10 companies on free/discounted access in exchange for real feedback and case studies — this is where your first credible customer proof comes from.
2. Public launch (Product Hunt, Hacker News, relevant dev communities) built around the "India-built, radically honest, fairly priced, no Silicon Valley markup" narrative — backed by real features, not just origin story.
3. Technical content as both credibility and distribution: engineering posts on the SLO burn-rate math, the causal-AI RCA design, the eBPF zero-instrumentation internals — this is the kind of content practitioners share and that ranks for the searches real buyers do.
4. Run the AI-agent-observability beachhead as a distinct acquisition motion feeding the full-platform expansion motion.
5. Iterate pricing using real usage data once it exists, rather than guessing twice.
6. Expand internationally once data residency (Phase 4) unlocks EU/regulated buyers.

---

## Metrics to track from Phase 2 onward

Track these regardless of team size — they tell you whether the product is actually becoming marketable, not just more feature-complete:

- Time-to-first-value: signup to first real trace/dashboard seen.
- Trial-to-paid (or trial-to-design-partner) conversion rate.
- Alert delivery success rate (should be 100% — this is binary, not a KPI to "improve").
- Weekly active usage per trial account (are they coming back, or did they look once).
- For the migration copilot specifically: % of imported dashboards/alerts that work without manual fixing.

## One honest note on sequencing

It will be tempting to jump to Phase 3's differentiation work first, since it's the most fun to build and the most fun to talk about. Resist that until Phase 0 is done and Phase 1 is at least functionally complete — a differentiated feature sitting next to a fake anomaly detector and silent alerts doesn't move the confidence number, it just adds one more thing a prospect can find broken during a trial.
