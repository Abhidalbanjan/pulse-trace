# PulseTrace — Feature Enhancement Specs

One spec per **sidebar feature**. Each describes where the feature stands today,
the gap versus a market-ready bar (Datadog / Grafana / New Relic / Backstage),
and a prioritized set of enhancements with user value, backend/frontend
touchpoints, and effort — so we can implement them one at a time.

These build on top of the completed ROAD_TO_100 (Waves 1–4). That work made each
pillar **present and competitive**; these specs take them from competitive to
**category-leading and demo-winning**.

## Index

| # | Feature | Route | Spec | Headline enhancement |
| --- | --- | --- | --- | --- |
| 1 | AI SRE (home) | `/` | [ai-sre.md](ai-sre.md) | Streaming, grounded, tool-transparent copilot with history |
| 2 | Incidents | `/incidents` | [incidents.md](incidents.md) | AI postmortems + similar-incident recall + MTTR analytics |
| 3 | Alerts | `/alerts` | [alerts.md](alerts.md) | Noise reduction: grouping, dedup, silences, composite rules |
| 4 | SLOs | `/slo` | [slos.md](slos.md) | Multi-window multi-burn-rate + budget-driven deploy freeze |
| 5 | Deploy Gates | `/deployments` | [deploy-gates.md](deploy-gates.md) | DORA metrics + auto-rollback + deploy markers everywhere |
| 6 | Onboarding | `/onboarding` | [onboarding.md](onboarding.md) | Guided per-language instrumentation + live first-data detector |
| 7 | Log Explorer | `/explorer` | [log-explorer.md](log-explorer.md) | Pattern clustering + facets + live tail + log-to-metric |
| 8 | Distributed Traces | `/traces` | [traces.md](traces.md) | First-class trace search + flame graph + service map |
| 9 | Services | `/services` | [services.md](services.md) | Per-service RED golden-signals dashboard + health score |
| 10 | Metrics | `/metrics` | [metrics.md](metrics.md) | Metric explorer + saveable dashboards + template variables |
| 11 | Error Tracking | `/errors` | [error-tracking.md](error-tracking.md) | Release health + assignment workflow + source context |
| 12 | Continuous Profiler | `/profiler` | [profiler.md](profiler.md) | Interactive flame graph + diff flame graph + more profile types |
| 13 | Real User Monitoring | `/rum` | [rum.md](rum.md) | Session timeline + frustration signals + RUM→trace linking |
| 14 | Synthetic Monitoring | `/synthetics` | [synthetics.md](synthetics.md) | Multi-region + browser checks + SSL/uptime + status page |
| 15 | Topology | `/topology` | [topology.md](topology.md) | Live traffic + time-travel + anomaly overlay |
| 16 | Catalog | `/catalog` | [catalog.md](catalog.md) | Production-readiness scorecards (Backstage/Cortex-style) |
| 17 | Settings | `/settings` | [settings.md](settings.md) | Usage dashboard + integrations UI + API tokens + SCIM/SAML admin |

## Spec template

Every spec follows the same shape:

1. **Where it stands** — honest current capabilities (grounded in the code).
2. **Market-ready gap** — what a buyer expects that isn't there yet.
3. **Proposed enhancements** — prioritized `E1…En`, each with: user value, what to
   build, backend + frontend touchpoints, and effort (S/M/L).
4. **Market-ready DoD** — the checklist that closes the feature.
5. **Suggested sequence** — the order to implement.

## Effort legend

- **S** — a focused day: one endpoint + one panel, no new datastore.
- **M** — a slice: new endpoint(s) + non-trivial UI, maybe a migration.
- **L** — multi-slice: new subsystem, worker, or data model.

## Cross-cutting conventions (apply to every enhancement)

- Tenant-scoped server-side (never from client headers); admin surfaces RBAC-gated.
- New backend route → UI consumer, or registered `uiNone` — keep the **parity gate at 100%**.
- Pure logic factored out and unit-tested; add e2e for the happy path.
- Secure-by-default, fail-closed; secrets encrypted at rest.
- Every gate green per slice (8 Go modules build/vet/test, FE tsc/eslint/build, parity, govulncheck).
