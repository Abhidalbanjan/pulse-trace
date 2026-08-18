# PulseTrace vs OpenObserve — Gap Analysis & Plan to Win Every Dimension

_Author: competitive engineering plan · Created: 2026-08-13_
_Subject: [openobserve/openobserve](https://github.com/openobserve/openobserve) (AGPL-3.0, Rust) — **source-verified** at `main`, Aug 2026, supplemented by openobserve.ai/docs._
_Implementation plan: [COMPETITIVE_OPENOBSERVE_IMPLEMENTATION.md](COMPETITIVE_OPENOBSERVE_IMPLEMENTATION.md) — the phase-by-phase "how"._
_Measurements: [BENCHMARK.md](../BENCHMARK.md) — D1, D2 and D3 are now **measured on a shared 2 GiB corpus**, not asserted._

> ⚠️ **Revised 2026-08-13 after a source-verification pass.** The first version of
> this document was written from five web pages and **overstated PulseTrace's
> position**: it claimed wins on SLOs, synthetics and anomaly detection that
> OpenObserve's source disproves. Every verdict below has been re-checked against
> the actual repository (1,419 Rust files / 576k LOC).
> **[COMPETITIVE_OPENOBSERVE_VERIFICATION.md](COMPETITIVE_OPENOBSERVE_VERIFICATION.md)
> carries the evidence with file citations and is the authority where the two disagree.**

This document does three things:

1. **Grounds both products honestly** — what PulseTrace actually is in this repo
   (not what the README claims), and what OpenObserve actually ships.
2. **Names every dimension where OpenObserve is genuinely better**, with the
   mechanism that makes it better — not a feature-name list.
3. **Gives an executable plan (Waves O0–O7)** that first reaches parity on those,
   then adds a *strictly-better delta* on every dimension, including the ones we
   already win.

It follows the house conventions of enhancements/README.md:
epics numbered `E1…En`, effort in S/M/L, explicit backend + frontend touchpoints,
a DoD per epic, and the parity gate stays at 100%.

---

## 0. Method and evidence base

| Side | How it was assessed |
| --- | --- |
| PulseTrace | Direct code read: 220 Go files across 8 modules, 66 `.tsx` screens/components, all HTTP routes extracted from the 6 services, `docker-compose.yml` (23 runtime containers), Quickwit index + Kafka source config, ClickHouse table DDL, [ROAD_TO_100.md](../ROAD_TO_100.md), [PARITY_REPORT.md](../PARITY_REPORT.md), [PERF_BASELINE.md](../PERF_BASELINE.md). |
| OpenObserve | **Source read** of a shallow clone (1,419 Rust files / 576k LOC): structural map of 38 crates, then targeted reads of the module owning each dimension, with file citations recorded in [COMPETITIVE_OPENOBSERVE_VERIFICATION.md](COMPETITIVE_OPENOBSERVE_VERIFICATION.md). Supplemented by the repo README and openobserve.ai docs. **Two limits:** their `src/enterprise/` is four 16-line stubs — the commercial build is closed-source, so SDR, SSO breadth and parts of federation are *not verifiable*; and their **published** performance/cost figures (140×, 2 PB/day) remain marketing. P0 has since settled the ones that matter to a buyer — storage, footprint and query latency are measured in [BENCHMARK.md](../BENCHMARK.md) and cited inline below; their headline claims are still unverified and are no longer relied on anywhere in this document. |

### 0.1 What PulseTrace actually is (correcting the README)

The README's diagram says logs land in ClickHouse. **They do not.** The real
topology, from the code:

- **Logs** → Kafka `logs` topic → **Quickwit** (`pulsetrace-logs`, `mode: dynamic`,
  VRL transform in [`quickwit/kafka-source.yaml`](../quickwit/kafka-source.yaml)).
  `log-service/internal/consumer/` is **empty** — there is no ClickHouse log
  consumer. All four log-ingest paths (native, OTLP via
  [`logbridge`](../gateway-service/internal/logbridge/logbridge.go), Datadog,
  Splunk HEC) converge on that one topic.
- **Traces / metrics** → OTel Collector → ClickHouse `otel_traces` / `otel_metrics`,
  queried by param-driven handlers that build SQL server-side.
- **RUM / synthetics** → ClickHouse app-owned tables, `PARTITION BY TenantID`,
  hardcoded `TTL … + INTERVAL 7 DAY`.
- **Control plane** → Postgres (alerts, incidents, SLOs, RBAC, billing, audit).
- **Topology** → Neo4j.

This matters for the plan: our log tier is **already object-store-native**
(Quickwit splits), which is closer to OpenObserve's economics than the README
suggests — but the **metrics/traces tier is SSD-resident ClickHouse**, which is
where the cost gap actually lives. Fix the README as part of O0.

### 0.2 What OpenObserve actually is

A single Rust binary that can run five roles (router, ingester, querier,
compactor, scheduler). Ingest → parse → schema-evolve → real-time alert eval →
WAL (hourly buckets, 128 MB files) → memtable (256 MB) → immutable snapshot →
**Parquet on object storage**; compactor merges to ≤2 GB files and enforces
retention. Queriers are stateless, cache Parquet in memory, and a leader querier
fans work out over gRPC. Metadata: SQLite (single node) or Postgres + NATS (HA).
Claims: 140× lower storage cost than Elasticsearch, 2+ PB/day at the largest
deployment, ~2.6 TB/day on a single node.

---

## 1. Head-to-head scorecard

Verdict is **today**, before any work in this plan.

| # | Dimension | PulseTrace today | OpenObserve today | Verdict |
| --- | --- | --- | --- | :---: |
| D1 | Time-to-first-data / deployability | **Measured: 23 containers, 5.00 GiB peak RSS.** 11 volumes, Kafka+ZK+Neo4j+RabbitMQ+Redis+CH+PG+Quickwit+MinIO+Azurite+Jaeger+Prom+Grafana+Pyroscope | **Measured: 2 containers, 733 MiB peak RSS.** One binary, SQLite + disk, no deps | **OO** — 11× the containers, 7× the memory |
| D2 | Storage economics | **Measured: 3.39 GiB written per GiB ingested.** Quickwit splits (good) + ClickHouse SSD hot for traces/metrics/RUM; S3/Azure/GCS only as *cold* tier | **Measured: 309 MiB per GiB ingested — 11.2× less.** Parquet-on-object-store as the **primary** tier, compaction, 99% search-space pruning | **OO** — measured on a corrected sampler; the two earlier runs under-measured *us* |
| D3 | Ad-hoc query power | **Measured: 2 of 6 benchmark query classes are not expressible at all.** No user query language. Logs = Quickwit query string; metrics = fixed params → server-built SQL; no joins, no aggregations, no user SQL | **6 of 6 expressible.** **DataFusion** SQL engine + a **full PromQL engine** (aggregations/binaries/functions) + tantivy inverted index + bloom pruner + result cache | **OO** — a capability gap, not a latency gap |
| D4 | Custom dashboards | **None.** No `/dashboards` route exists | Folders, tabs, **21 panel types (verified)**, variables + dependencies, filters, time-comparison, drilldown, import/export, timed annotations | **OO** |
| D5 | Streams / schema flexibility | Fixed tables; Quickwit index is `mode: dynamic` but nothing surfaces the inferred schema; TTLs hardcoded in DDL; no per-tenant retention | Stream registry + schema inference; `StreamSettings` carries **15 fields** — FTS keys, partition keys, bloom-filter fields, UDS, retention, extended retention, `index_all_values`, `store_original_data`, `max_query_range` | **OO** |
| D6 | Ingest pipelines / transforms | Vector in compose + one VRL script in the Quickwit source — no user-facing product | Real-time + scheduled pipelines, no-code graph editor, **the real `vrl` 0.31 crate**, enrichment tables, remote destinations, import/export | **OO** |
| D7 | Ingestion source breadth | OTLP (gRPC+HTTP), native JSON, **Datadog** logs + v0.3/0.4/0.5 traces, Splunk HEC | Elasticsearch `_bulk`, **Splunk HEC (`_hec`)**, Prometheus remote-write, Kinesis Firehose, GCP pub/sub, Loki push, OTLP, session-replay. **No Datadog; neither side has a syslog listener** | **OO** |
| D8 | Scheduled reports | None | Scheduled dashboard reports (email/PDF) | **OO** |
| D9 | Session replay | Lightweight event timeline (RUM E1) | Full session replay | **OO** |
| D10 | LLM/AI observability *of the customer's apps* | None (we use AI, we don't monitor theirs) | LLM monitoring + `llm_evaluations/` (eval jobs, evaluator trace exporter) + an MCP tool server | **OO** |
| D11 | Data controls at ingest | 4-regex global PII middleware ([`pii/sanitizer.go`](../gateway-service/internal/pii/sanitizer.go)) | Cipher-key registry in OSS; **SDR itself is behind a 16-line enterprise stub — unverifiable** | **OO** (unverified) |
| D12 | Multi-region / federation | Single region (admitted in DISASTER_RECOVERY.md) | Super Cluster: federated search across clusters/regions | **OO** |
| D13 | Auth breadth | OIDC, SAML 2.0, SCIM 2.0, TOTP MFA, RBAC + ABAC, session revocation, password lifecycle | OSS has JWT only; OIDC/SAML/LDAP live in the 16-line `o2_dex` stub — **unverifiable** | **OO** (unverified) |
| D14 | Compliance certifications | None claimed | SOC 2 Type II, ISO 27001, GDPR, HIPAA-ready | **OO** |
| D15 | Proven scale | Published p50/p95/p99 for the ingest path at a *permitted* rate; ceiling undocumented | 2+ PB/day claimed at largest deployment | **OO** (claimed) |
| D16 | Incident lifecycle & RCA | Correlation → incidents → causal RCA with a **CI-gated eval harness at 90.9%** → AI postmortems → self-healing playbooks (dry-run / approve / reject, risk-tier authz, hallucination guardrail) | Incidents + incident-event tables, an **810-line workflow engine**, action scripts, an MCP tool server, a `sys_rca_agent` service account | **contested** — our delta is the approval gate + eval harness, not automation itself |
| D17 | SLO engineering | Multi-window multi-burn-rate, budget forecasting, PR-time SLO evaluation | **Full SLO subsystem** — evaluate / reconcile / **backfill** / writer, `slo_budget_charges`, burndown + PromQL-preview + time-slice UI | **OO** — they have backfill and reconciliation we lack |
| D17b | Deploy intelligence | Deploy gates, DORA, change-failure linking, PR-time SLO evaluation | No counterpart found in source | **PT** |
| D18 | APM pillar depth | **Continuous profiling** (flat + diff flame graph), error tracking (clustering, regression alerting), Neo4j topology (blast radius), Backstage-style catalog | Traces + service map + RUM + **synthetics** + **anomaly detection (2,283 L)**; their flame graph renders trace spans — **no profiling pillar** | **PT on profiling, catalog, topology** |
| D19 | Deletability & GDPR | Tenant purge across every store + deletion certificate + closed account | Confirmed in source: file-level GC only (`compaction/deleted.rs`, `pending_delete.rs`); **no user-facing record deletion, no erasure path** | **PT** |
| D20 | Audit integrity | Hash-chained tamper-evident audit + server-side verify + NDJSON export | `src/audit/src/lib.rs` is **105 lines** — a publisher to a stream. No hash chain, no verify | **PT** |
| D21 | Engineering discipline | Parity CI gate at 100% (164 routes, 0 orphans), causal eval gate, k6 perf gate, govulncheck at zero | Not published | **PT** |

**Score after verification: OpenObserve leads ~14, PulseTrace leads 4 confirmed
(D17b, D18-profiling/catalog/topology, D19, D20) plus D21, with D16 contested.**
The pre-verification draft claimed 15–6; three of those six were wrong. Two
dimensions (D11, D13) are *unverifiable* from the OSS repo because the enterprise
crates are stubs.

The 14 are not equal — D1–D6 are what a buyer hits in the first 20 minutes, and
they decide the evaluation.

### 1.1 The strategic read

PulseTrace is a **deep incident-intelligence platform on a heavyweight stack**.
OpenObserve is a **light, cheap, flexible telemetry substrate — that has also
been quietly building the intelligence layer** (SLOs, synthetics, anomaly
detection, workflows, an MCP tool server, LLM evaluations). That is the real
finding of the verification pass, and it is worse news than the original draft:
the window in which "they're just a log tool" is true has closed.

We still win once a buyer is doing incident work *the way we do it* — approval-
gated remediation, measured RCA, deploy intelligence. They win before that, in
evaluation, on install time, storage bill, and "can I just ask it a question."
The strategy is unchanged; the urgency is higher and the moat is narrower than
§3 originally claimed.

So the plan is not "add their features." It is: **make our substrate as cheap,
fast and self-serve as theirs, then keep the incident-intelligence layer they
don't have.** A telemetry substrate is table stakes; the intelligence on top is
the product. Waves O1–O5 buy the table stakes. Waves O6–O7 make the win durable.

### 1.2 What P0 actually measured — and what it changed

Three of the rows above used to be argument. They are now numbers, taken on a
shared byte-reproducible 2 GiB corpus (5,104,768 log records ingested and
verified on both sides), equal 4 CPU / 8 GiB caps, OpenObserve pinned to
v0.14.4, 20 iterations per query class. Full method and results:
[BENCHMARK.md](../BENCHMARK.md).

| Dimension | Was | Is |
| --- | --- | --- |
| D1 deployability | "23 containers vs one binary" | **23 vs 2 containers; 5.00 GiB vs 733 MiB peak RSS** |
| D2 storage | their "140× cheaper than Elastic" | **3.39 GiB vs 309 MiB per GiB ingested — 11.2×** |
| D3 query power | "no query language" | **2 of 6 classes not expressible** — the user cannot ask, at any latency |

Three findings change the plan rather than merely confirming it:

1. **The storage gap is amplification, not architecture.** Our log tier is
   Quickwit, which *is* tantivy — the same substrate as their index. So this is
   not "columnar beats inverted index." Kafka retains a full second copy of the
   corpus and the Quickwit splits are never compacted. Both are fixable without
   replacing an engine, which **shrinks P2 from an object-store-primary rewrite
   to deduplication plus compaction**. The six weeks budgeted to answer this
   question are largely answered.
2. **Latency is not the problem; expressibility is.** Where both sides can run
   the query, only one result is robust across runs: **trace-by-ID is ~45×
   faster on our side** (13 ms vs 579 ms p50, comparing default configurations —
   `trace_id` could be indexed on theirs). The other three expressible classes
   sit close enough that **run-to-run variance exceeds the gap**: the previous
   run had us winning the time-narrowed service filter on p95, this one has them
   ahead, with nothing changed in either product. An earlier draft of this
   section claimed we win two of four; on the corrected evidence it is one, and
   the rest should not be quoted at all. We lose the two classes we cannot
   express. That is a **P3 (query engine) problem, and only P3's** — no tuning
   closes it, and no tuning is needed elsewhere.
3. **One architectural decision shows up in two measurements.** Ingest takes
   572 s against their 74 s, and the same gateway → Kafka → Quickwit hop is what
   duplicates the corpus on disk. Fixing it pays down D2 and ingest throughput
   together, which makes it the highest-leverage item in the substrate work.

**What was not measured:** cold start to first query, and every claim about
their behaviour above single-node 2 GiB scale. Their 2 PB/day figure remains
uncorroborated and is not relied on here.

---

## 2. Where OpenObserve is genuinely better — mechanisms, not features

**G1 · One binary vs 23 containers — measured.** `docker stats` during the
benchmark: **23 containers and 5.00 GiB resident on our side, 2 and 733 MiB on
theirs.** Our compose brings up ZooKeeper, Kafka,
Neo4j, ClickHouse, Postgres, RabbitMQ, Redis, Jaeger, OTel Collector, Prometheus,
Grafana, Vector, Quickwit (+ setup), MinIO (+ mc), Azurite, Pyroscope, plus 7
first-party services. Every one is a failure mode, a RAM allocation, and a reason
an evaluator quits. OpenObserve's evaluator is running in under a minute with
`docker run`, and the *same binary* is the HA deployment with roles turned on.

**G2 · Object store as the primary tier, not the cold tier.** Our
`storage.xml` tiers ClickHouse to S3/Azure/GCS on age. OpenObserve never has a
hot SSD tier at all — Parquet lands in object storage within seconds, queriers
are stateless and cache on demand, and the compactor rewrites to ≤2 GB files.
That lets them scale queriers independently of data, and it shows up on
disk: **309 MiB per GiB ingested against our 3.39 GiB — 11.2×** (their "140×
cheaper than Elastic" is marketing and is not the source of that number; ours
is). Our traces/metrics path pays SSD prices for the whole retention window.
But note what the measurement also found: the log-tier share of our overhead is
**duplication, not format** — Kafka keeps a second copy and splits are never
compacted — so closing most of D2 does not require adopting their architecture.

**G3 · No query language is a hard ceiling.** Every question a user can ask us
has to have been anticipated by a Go handler. `metrics_handler.go` supports
`rate`/`p50`/`p90`/`p95`/`p99` because someone wrote a `switch`. A user who wants
"error rate by customer tier joined against deploys, bucketed 5m" simply cannot
ask. OpenObserve users write SQL. This is the single largest capability gap and
it is invisible in a feature checklist — the benchmark makes it visible:
**2 of 6 query classes return "not expressible" for PulseTrace**, while the four
that do run are close enough that only trace-by-ID separates the products. The
ceiling is what a user may ask, not how fast we answer.

**G4 · No dashboards means no daily habit.** We have 17 opinionated screens and
zero user-authored views. Dashboards are how an observability tool becomes the
tab someone leaves open, how it spreads from the on-call engineer to the team
lead, and how a migration from Grafana/Datadog is even conceivable. Its absence
also caps D8 (reports) and D17's visibility.

**G5 · No stream abstraction means no self-service data.** Quickwit is already in
`mode: dynamic`, so arbitrary JSON fields *are* indexed — but no API or screen
exposes the inferred schema, per-stream retention, FTS keys, or partition keys.
Retention is `INTERVAL 7 DAY` compiled into DDL. A customer cannot say "keep
audit logs 400 days, app logs 14."

**G6 · Pipelines are where competitors capture the ingest path.** Whoever owns
the transform/route/enrich step owns the data. OpenObserve has real-time and
scheduled pipelines with a visual editor, enrichment tables, remote destinations,
and — verified in `Cargo.toml:612` — **the real `vrl = 0.31` crate**, not a
dialect of their own. We have a VRL snippet buried in a Quickwit source file that
no user will ever see.

> ⚠️ This is worse for us than it looks. They embed Vector's actual VRL
> implementation. A Go-native "VRL-compatible subset" (the original P6.3 plan)
> will *always* lag it, and a subset that silently mis-evaluates a pasted snippet
> is worse than no compatibility. Either run VRL in a sidecar or stop claiming
> compatibility — see the correction in [the verification appendix](COMPETITIVE_OPENOBSERVE_VERIFICATION.md).

**G7 · Ingestion breadth is the migration moat.** Verified: they ship `_bulk`,
**`_hec` (Splunk HEC)**, `_kinesis_firehose`, GCP pub/sub, Loki push, OTLP and
Prometheus `remote_write`. So Splunk is **not** the differentiator the first
draft claimed — **Datadog is the only migration path unique to us**, and it is
worth defending. The gaps to close are `_bulk` and `remote_write`, which unlock
the two largest installed bases; Fluent Bit / Filebeat / Telegraf are what
actually runs on customer fleets. Neither product has a syslog listener — that
one is open for us to take.

**G8 · SDR vs four regexes.** [`pii/sanitizer.go`](../gateway-service/internal/pii/sanitizer.go)
runs 4 hardcoded patterns on every request body, globally, unconfigurable, and
one of them (`{13,19}` digits) will corrupt legitimate numeric payloads. A
regulated buyer needs per-field, per-stream, configurable, auditable redaction —
so this remains a real gap for us **regardless** of what they ship. Their SDR
itself sits behind a 16-line enterprise stub and could not be verified.

**G9 · Federated search.** Their Super Cluster answers one query across regions.
Our DR doc honestly says single-region. Any buyer with EU + US data residency
requirements is currently unservable.

**G10 · LDAP/AD.** We have OIDC + SAML + SCIM, which covers modern IdPs, but a
large slice of on-prem enterprise is still LDAP-bound. (Their SSO federation
lives in the 16-line `o2_dex` stub, so the OSS repo proves nothing here — this
gap is real for us on its own merits, not because they beat us to it.)

**G11 · Certifications.** SOC 2 Type II / ISO 27001 / HIPAA BAA gate procurement
regardless of how good the code is. Not a code task — a program with a start date.

**G12 · Independently proven scale.** Their number is public. Ours is a per-PR k6
gate at a rate deliberately *under* the tenant limiter — good regression hygiene,
not a scale claim. We have no published throughput ceiling.

---

## 3. Where PulseTrace already wins — protect and widen

Do not trade any of these away while chasing G1–G12. **This list is half the
length of the pre-verification draft** — everything below survived a source read.

**Confirmed, citable:**

- **Continuous profiling.** No profiling pillar on their side; their only
  `profiling.rs` is server self-instrumentation and their flame graph renders
  trace spans. Flat profile + diff flame graph are ours alone.
- **Deletability.** Verified in source: their deletion paths are internal
  compacted-file GC, with no user-facing record deletion and no erasure path. We
  ship a per-tenant purge with a deletion certificate. Against GDPR Art. 17 this
  is not a feature difference, it is a compliance difference. **Make it a headline.**
- **Tamper-evident audit chain.** Their `src/audit/src/lib.rs` is 105 lines — a
  publisher to a stream, no hash chain, no verify endpoint.
- **Deploy intelligence.** Deploy gates, DORA, change-failure linking, PR-time
  SLO evaluation — no counterpart found.
- **Service catalog and the Neo4j dependency graph** (blast radius, causal paths).
- **The parity gate.** 164 routes, 0 orphans, enforced in CI. Internal, but it is
  why every wave below can be trusted to actually ship UI.

**Contested — narrower than we thought:**

- **Remediation.** We have the approval gate, risk-tier authz and dry-run. They
  have an 810-line workflow engine, action scripts and an MCP tool server. The
  moat is *governance*, not automation. Do not claim "they have nothing."
- **Causal RCA with a CI-enforced accuracy gate** (90.9%). The measurement
  discipline still looks unique — but they ship `llm_evaluations/` and a
  `sys_rca_agent`, so lead with **"measured RCA"**, not "RCA."

**Deleted from this list after verification — they have these:** SLOs and error
budgets (deeper than ours), synthetics, anomaly detection.

---

## 4. The plan

Eight waves. O0 is a prerequisite for honest claims; O1–O2 are the hard ones and
carry the most value; O3–O5 are large but mechanical; O6–O7 make the lead durable.

Cross-cutting rules (per enhancements/README.md):
tenant-scoped server-side, admin surfaces RBAC-gated, every new route gets a UI
consumer or a `uiNone` registration, pure logic unit-tested, one e2e per flow,
fail-closed, all gates green per slice.

---

### Wave O0 — Measure before claiming · effort M ✅ *mostly delivered*

Nothing in this document should be asserted publicly until it is measured.

**O0.1, O0.2 and O0.4 are done.** The harness is in `scripts/bench/`, the
scoreboard is [BENCHMARK.md](../BENCHMARK.md), and the README diagram is
corrected. §1.2 above records what the numbers changed. **O0.3 (throughput
ceiling) and cold-start-to-first-query remain open** — the latter is the one
sample in O0.1's list the harness still does not collect.

| Epic | What to build | Effort |
| --- | --- | :---: |
| **O0.1** | **Head-to-head bench harness** in `scripts/bench/`: bring up OpenObserve (single binary) and PulseTrace side by side, replay an identical corpus (10 GB mixed logs/traces/metrics), and record: bytes-on-disk per GB ingested, ingest throughput ceiling per node, query p50/p95/p99 across 6 query classes (needle-in-haystack, full-scan aggregation, high-cardinality group-by, trace-by-id, metric range-query, regex), cold-start seconds, container count, idle + loaded RSS. | M |
| **O0.2** | **`BENCHMARK.md`**, machine-written by the harness (same discipline as [PERF_BASELINE.md](../PERF_BASELINE.md)) — never hand-edited. Publish losses as loudly as wins; the file is the scoreboard for this whole plan. | S |
| **O0.3** | **Find our real throughput ceiling**: dedicated tenant, limiter raised, ramp to failure, document the knee and the binding constraint per protocol. Closes G12. | S |
| **O0.4** | **Fix the README architecture diagram** — logs go to Quickwit, not ClickHouse. Shipping a wrong diagram in the front door of the repo costs credibility in exactly the evaluation we are trying to win. | S |

**DoD:** `BENCHMARK.md` exists, is regenerated by one command, and every claim in
§1 of this document is replaced by a measured number. **Met for D1, D2 and D3.**

---

### Wave O1 — Deployability and economics (closes G1, G2) · effort L

The highest-leverage wave. Two independent tracks.

#### O1.1 — `pulsetrace-lite`: single-container mode · effort L

One container, no external dependencies, working UI in <60 s, honest degradation.
Same binaries, different wiring — **not a fork**:

| Dependency today | Lite substitution | Mechanism |
| --- | --- | --- |
| Kafka + ZooKeeper | in-process channel bus | `shared/kafka` gets a `Bus` interface; the existing producer/consumer become one implementation, an in-memory bounded queue with disk spill becomes the other |
| Postgres | embedded SQLite | migrations already live per service; add a dialect shim in `shared/db` and a `sqlite` build tag |
| Neo4j | Postgres/SQLite adjacency tables | topology-service repo interface behind a `GraphStore` port; the graph is small (services × edges), Cypher usage is shallow |
| RabbitMQ | same in-process bus | notification worker already consumes an interface |
| Quickwit | Quickwit **embedded as a library-mode process** in the container, or `tantivy` directly | keep the index format identical so lite→HA is a data-compatible upgrade |
| ClickHouse | `chdb` (embedded ClickHouse) or DuckDB over the same Parquet | query SQL is generated in one place per handler; target the same dialect |
| MinIO / Azurite | local filesystem, S3 optional | already configurable |
| Jaeger / Prometheus / Grafana | not shipped in lite | our own UI already covers these; they are dev conveniences |

- **Backend:** `shared/bus` port + two adapters; `shared/db` dialect shim;
  `GraphStore` port in topology-service; a `PULSETRACE_MODE=lite|cluster` switch
  resolved once at startup in each `cmd/main.go`; a single `Dockerfile.lite` with
  a supervisor that runs all six services in one container (or one binary with
  role flags — preferred, mirroring OpenObserve's router/ingester/querier model).
- **Frontend:** a mode badge in Settings; features unavailable in lite render as
  explicit "requires cluster mode" states, never as fake success.
- **Tests:** an e2e that boots lite from a clean volume and asserts first-data <60 s;
  a data-compat test that a lite volume upgrades into cluster mode.
- **DoD:** `docker run -p 8080:8080 pulsetrace/pulsetrace` → login → send OTLP →
  see the log in Explorer, on a laptop, with no other process running.

#### O1.2 — Object-store-primary telemetry tier · effort L

- **Backend:** move traces/metrics/RUM/synthetics off SSD-primary ClickHouse to
  the same posture as our logs: recent data in a small hot window, everything
  else as Parquet/splits on object storage with a compaction job. Two viable
  routes — (a) ClickHouse `MergeTree` with an *aggressive* `TTL … TO DISK 's3'`
  (hours, not days) plus `allow_experimental_object_type` for wide rows, or
  (b) write Parquet directly and query via chDb/DuckDB. **Recommend (a) first**:
  it is a storage-policy change plus a benchmark, not a rewrite. **O0.1 has since
  reported: the 11.2× is duplication, not format** — so (a) is not merely the
  cheaper bet, it is the correct one, and (b) is now out of scope.
- **Backend:** a **compactor** worker (small-part merge + retention enforcement +
  file-list index), the piece OpenObserve has and we do not.
- **Backend, do this first:** **bound Kafka retention.** O0.1 answered the
  double-cost question and found a bigger one — the `logs` topic keeps a full
  second copy of every record after Quickwit has indexed it, and that same hop is
  why our ingest takes 572 s to their 74 s. One config change, measurable in a
  re-run, paying down two dimensions at once. Then close the original question:
  whether Quickwit splits and ClickHouse `otel_logs` overlap, and drop the
  unqueried copy.
- **Backend:** **compact Quickwit splits.** The index is configured once by an
  init container and never merged, so file count grows without bound — the second
  named cause of the 11.2×, and the one no ClickHouse tiering change touches.
- **Frontend:** Settings → Storage: bytes by tier, by stream, by tenant;
  projected monthly cost; the compaction backlog.
- **DoD:** `BENCHMARK.md` shows bytes-on-disk per GB ingested **at or below**
  OpenObserve's for the same corpus, with query p95 no worse than 1.2×.

---

### Wave O2 — Query power (closes G3) · effort L

#### O2.1 — Federated SQL surface · effort L
- **Backend:** `POST /api/v1/query/sql` — a **sandboxed** SQL endpoint. Parse with
  a real SQL parser (not regex); allow `SELECT` only; reject DDL/DML/system
  tables/`file()`/`url()`/`remote()`; **inject the tenant predicate at the AST
  level** and reuse the existing `queryScoped` fail-closed guard from F0.3 (the
  ratchet test `TestNoRawTenantTableReads` must be extended to cover this path);
  enforce per-role row/byte/time-budget limits; stream results.
- **Backend:** one virtual catalog across stores — `logs` (Quickwit), `traces`,
  `metrics`, `rum`, `synthetics` (ClickHouse), so a user writes one dialect and
  the planner routes/joins. Start with single-store queries; cross-store joins in
  a second slice.
- **Frontend:** a **Query** screen — editor with schema-aware autocomplete,
  result grid, one-click "visualize" (feeds O3), save-as (extends `saved_searches`),
  and a shareable URL. Also embed it as the "Advanced" mode of Explorer and Metrics.
- **Tests:** an injection/escape suite (the security-critical one), a cross-tenant
  denial suite, budget-limit enforcement, golden-result tests per store.
- **DoD:** every question the 17 screens can answer is also expressible in SQL,
  and no SQL can read another tenant's row. **Effort L, security-critical — this
  epic gets a dedicated security review.**

#### O2.2 — PromQL for metrics · effort M
- **Backend:** `GET /api/v1/query` + `/query_range` implementing the Prometheus
  HTTP API over our ClickHouse `otel_metrics` (selector → SQL, then the PromQL
  function/operator layer). This also makes PulseTrace a **drop-in Grafana
  datasource**, which is a migration wedge OpenObserve already has.
- **Frontend:** PromQL mode in Metrics alongside the builder; the existing
  `rate`/quantile functions become a builder that *emits PromQL*.
- **DoD:** point Grafana at PulseTrace as a Prometheus datasource; existing
  dashboards render.

#### O2.3 — Query acceleration · effort M
- **Backend:** result cache + histogram/aggregation cache keyed by
  (tenant, query, time-bucket) in the **Redis already in compose but barely used**;
  query partitioning across time so long ranges stream progressively; a `search
  around` API (we have `/logs/{id}/context` — generalize it to any store).
- **Frontend:** progressive result rendering with a partial-results indicator.
- **DoD:** repeat-query p95 drops ≥5×; the histogram no longer re-scans.

---

### Wave O3 — Dashboards & reports (closes G4, G8) · effort L

#### O3.1 — Dashboard subsystem · effort L
- **Backend:** `dashboards`, `dashboard_folders`, `dashboard_panels`,
  `dashboard_variables` tables (tenant-scoped, RBAC'd); CRUD + versioning +
  JSON import/export; a panel-query executor built on O2.1.
- **Frontend:** new `/dashboards` route — folders + tabs, drag-resize grid,
  panel editor (query → viz → options), **template variables with dependencies**,
  dashboard-level filters, time-range + **time comparison** (vs previous period),
  panel drilldown to Explorer/Traces with context carried, annotations (deploy
  markers from F5 reused as the first annotation source).
- **Panels (target ≥20, beating their 19):** time series, bar, stacked bar,
  horizontal bar, area, line, scatter, pie, donut, gauge, stat/single-value,
  heatmap, table, log stream, top-list, histogram, sankey, geomap, treemap,
  service map, flame graph (reuse F12), trace waterfall (reuse F7).
- **DoD:** a user builds a 6-panel dashboard with two variables, shares a URL,
  exports and re-imports the JSON.

#### O3.2 — Migration importers · effort M
- **Backend:** Grafana dashboard JSON → PulseTrace dashboard (panel + PromQL
  translation, feasible once O2.2 lands); Datadog dashboard JSON → same.
- **Frontend:** Onboarding → "Import from Grafana / Datadog", with a per-panel
  translation report (what converted, what needs hand-fixing — honest, not silent).
- **DoD:** a real Grafana dashboard imports with ≥80% of panels rendering.

#### O3.3 — Scheduled reports · effort M
- **Backend:** a scheduler (correlation-service already runs workers) that renders
  a dashboard headlessly to PDF/PNG on a cron and dispatches through the existing
  F3 notification channels; report definitions tenant-scoped.
- **Frontend:** Dashboards → Schedule report (cadence, recipients, time range,
  format) + a run history with the last artifact.
- **DoD:** a weekly PDF lands in email and Slack.

---

### Wave O4 — Streams & pipelines (closes G5, G6, G8-adjacent) · effort L

#### O4.1 — Streams as a first-class object · effort M
- **Backend:** a stream registry (`streams` table + a schema-inference reader over
  Quickwit's dynamic mapping and ClickHouse columns): list streams, doc count,
  ingested bytes, time range, inferred fields + types; per-stream settings —
  **retention** (replaces the hardcoded 7-day TTLs, enforced by the O1.2
  compactor), full-text-search keys, partition keys, user-defined schema,
  extended retention for specific fields.
- **Frontend:** Settings → **Streams**: the list, the schema explorer, per-stream
  settings, per-stream cost. Retention becomes self-serve.
- **DoD:** a tenant sets 400-day retention on `audit` and 14-day on `app`, and it
  is enforced and billed correctly.

#### O4.2 — Ingest pipelines · effort L
- **Backend:** a pipeline engine on the ingest path (gateway → before Kafka
  publish) with a node graph: source (stream) → condition → transform (VRL) →
  enrichment (lookup table) → destination (stream / remote HTTP / S3 / Kafka).
  Embed a VRL runtime (`vrl` is a Rust crate — either shell out to a sidecar or
  implement a Go-native subset; **recommend a compatible subset first**, chosen so
  OpenObserve VRL snippets mostly paste in). Real-time pipelines run inline;
  scheduled pipelines run as a worker over stored data.
- **Backend:** enrichment tables (CSV/JSON upload, tenant-scoped, cached).
- **Frontend:** `/pipelines` — a visual node editor with a **live preview panel**
  (paste a sample event, see it flow through each node). This is where we can beat
  them outright: their editor is a graph; ours is a graph **with a debugger**.
- **DoD:** a user drops a noisy field, routes 5xx to a separate stream, enriches
  with a service→team lookup, and previews it on a real event before saving.

#### O4.3 — Real Sensitive Data Redaction (closes G8) · effort M
- **Backend:** replace the global 4-regex middleware with per-stream, per-field
  redaction rules (mask / hash / drop / tokenize), evaluated in the O4.2 pipeline;
  named detector library (email, card + Luhn check, SSN, IBAN, JWT, private key,
  cloud creds); every redaction emits an audit event; **fail-closed** — a stream
  marked `sensitive` refuses ingest if its rule set fails to compile.
- **Frontend:** Settings → Data Protection: rules, a tester (paste → see the
  redaction), and a coverage report per stream.
- **DoD:** the current `{13,19}`-digit regex that corrupts payloads is gone, and
  redaction is configurable, testable, and audited.

---

### Wave O5 — Ingestion breadth (closes G7) · effort M

Each is a decoder in the existing `gateway-service/internal/ingestproxy/` pattern,
converging on the same Kafka topic — the Datadog/Splunk work already proved it.

| Epic | Protocol | Why it matters | Effort |
| --- | --- | --- | :---: |
| **O5.1** | **Elasticsearch `_bulk` + `_search` (subset)** | The single largest displacement pool; Filebeat/Logstash/Fluent Bit `es` output all speak it. `_search` subset lets existing Kibana-ish tooling read us. | M |
| **O5.2** | **Prometheus `remote_write`** (+ `remote_read`) | Every Prometheus in the world can dual-write to us with three lines of YAML. Pairs with O2.2 to make us a Prometheus drop-in on both ends. | M |
| **O5.3** | **Fluent Bit / Fluentd / Filebeat / Vector / Telegraf** recipes | Mostly config + docs on top of O5.1/native; ships as copy-paste blocks in Onboarding (the F-onboarding snippet system already exists). | S |
| **O5.4** | **AWS Kinesis Firehose + CloudWatch Logs subscription** | The default AWS log path; unlocks accounts that never install an agent. | M |
| **O5.5** | **Syslog (RFC 3164/5424, TCP/UDP/TLS) + journald** | Network gear, appliances, legacy fleets. The Quickwit source already grok-parses syslog — surface it as a real listener. | S |
| **O5.6** | **Loki push API** | Cheap; catches Grafana-stack migrations mid-flight. | S |
| **O5.7** | **OTLP/Arrow + gzip/zstd everywhere** | Cost per GB shipped — an efficiency win they do not advertise. | S |

**DoD:** Onboarding lists ≥12 ingestion sources, each with a tested copy-paste
config and an e2e proving data lands and is queryable.

---

### Wave O6 — Enterprise & scale (closes G9, G10, G11, G12) · effort L

- **O6.1 · Federated search (M→L).** A `LEADER`/`WORKER` query mode: fan a query
  to peer clusters over gRPC, merge, attribute per-cluster latency and partial
  failures **honestly in the UI** (never silently drop a region). Region-pinned
  storage per tenant for data residency.
- **O6.2 · Query & workload management (M).** Per-role/per-tenant query
  concurrency, byte/row/time budgets, a queue with priority, a **kill-query**
  control, and a slow-query log. (Their enterprise has this; ours can be in OSS.)
- **O6.3 · LDAP/AD (S).** One more provider behind the existing auth interface
  that OIDC and SAML already share.
- **O6.4 · BYOK / customer-managed keys (M).** Extend the existing AES-256-GCM
  posture (channels, MFA secrets) to a tenant-scoped KEK from KMS/Vault, with
  rotation and a re-wrap job.
- **O6.5 · Compliance program (L, non-code).** SOC 2 Type II readiness pack: the
  hash-chained audit (F20), the tenant-isolation ratchet (F0.3), DR (F21) and the
  parity/security gates are already the evidence — assemble the control mapping,
  pick an auditor, set a date. HIPAA BAA and ISO 27001 follow the same evidence.
- **O6.6 · Multi-region active/standby (L).** The deferred item in
  DISASTER_RECOVERY.md; O6.1 makes it worth doing.

---

### Wave O7 — Widen the moat: be *better*, not equal · effort L

Parity is not the goal. For each dimension we are about to tie on, ship the delta
that makes tying impossible.

| Epic | Delta over OpenObserve | Effort |
| --- | --- | :---: |
| **O7.1 · Pipeline debugger** | Their pipeline editor saves and hopes. Ours previews a real event through every node, shows per-node drop counts and error rates in production, and supports **replay of a rejected batch** after a fix. | M |
| **O7.2 · AI dashboard authoring** | Their AI generates a *query*. Ours generates a **whole dashboard** from a question ("show me checkout health"), grounded in the real schema from O4.1 and the service catalog, with every panel's SQL shown and editable — reusing the F15 hallucination guardrail so it can't invent a field. | M |
| **O7.3 · Full-fidelity session replay** | rrweb-grade DOM replay, but linked: replay ↔ RUM session ↔ backend trace ↔ logs ↔ profile, scrubbing one scrubs all. They have replay; nobody has replay wired to a flame graph. | L |
| **O7.4 · LLM observability, done properly** | Their LLM monitoring is prompts/tokens/latency/cost. Ours adds OTel GenAI semconv ingest + **cost attribution per feature/tenant**, eval-score tracking, guardrail-violation alerting, prompt-version regression diffs, and RCA over LLM incidents via the existing causal engine. | L |
| **O7.5 · Closed-loop remediation** | Today: dry-run → approve → execute. Add **post-execution verification against the SLO** and **auto-revert on failure**, with the whole loop in the incident timeline. Nothing else on the market closes the loop with a verification gate. | M |
| **O7.6 · One-click pivot matrix** | Make every pillar reachable from every other in one click with time+service context preserved: log→trace→profile→RUM session→deploy→incident→SLO. We have the pieces (F6/F7/F12); make it a guaranteed invariant with an e2e test per edge. | M |
| **O7.7 · Deletability as a headline** | Their docs concede data is immutable. Ship **per-record and per-subject deletion** (GDPR Art. 17) with a signed deletion certificate, and put it on the comparison page. This is a compliance capability they architecturally cannot match quickly. | M |
| **O7.8 · Cost intelligence** | Per-stream, per-team, per-query cost attribution, a "top 10 most expensive queries/streams" view, and pipeline-based volume-reduction suggestions with projected savings. They compete on being cheap; we compete on **showing you why you're spending**. | M |

---

## 5. Dimension-by-dimension: how we end up strictly better

| # | Dimension | After which wave | Why PulseTrace then wins outright |
| --- | --- | --- | --- |
| D1 | Deployability | O1.1 | One container like theirs, **plus** the same artifact scales to the cluster topology with role flags and a data-compatible upgrade path. Measured baseline to beat: **23 containers / 5.00 GiB RSS → their 2 / 733 MiB** |
| D2 | Storage economics | O1.2 (+ O0.1 ✅) | **Deduplicate the ingest path and compact splits first** — measured cause of the 11.2×, and cheaper than the tiering work. Then object-store-primary + compaction, **plus** O7.8 cost attribution — cheap *and* explains the bill |
| D3 | Query power | O2.1–O2.3 | SQL + PromQL like theirs, **plus** cross-store joins (logs ⋈ traces ⋈ deploys) they cannot do across separate stores. Measured gap is **expressibility, not latency** — 2 of 6 classes unaskable; of the 4 that run, only trace-by-ID separates the products (~45×, ours) and the rest are inside run-to-run variance |
| D4 | Dashboards | O3.1 | **≥22 panel types (they have 21, verified — not 19)**, plus flame-graph and trace-waterfall panels no log tool has, plus O7.2 AI authoring. Compete on *kind*, not count |
| D5 | Streams/schema | O4.1 | Same self-serve stream settings, **plus** per-stream cost attribution |
| D6 | Pipelines | O4.2 | Same node graph and VRL, **plus** O7.1 live debugger and production per-node telemetry |
| D7 | Ingestion breadth | O5 | Their sources **plus Datadog** (verified unique to us) **plus syslog** (neither side has it). Splunk HEC is *not* a differentiator — they ship `_hec` |
| D8 | Reports | O3.3 | Scheduled reports **plus** delivery through the existing multi-channel (Slack/PD/Opsgenie/webhook) fabric, not just email |
| D9 | Session replay | O7.3 | Replay wired to traces, profiles, and logs |
| D10 | LLM observability | O7.4 | They already ship `llm_evaluations/` — so **drop "eval scores" as the delta**. The delta is per-feature/per-tenant **cost attribution** and causal RCA over LLM incidents |
| D11 | Data controls | O4.3 | Configurable per-field SDR **plus** audited redaction events **plus** actual deletion (D19) |
| D12 | Federation | O6.1 | Federated search **plus** honest partial-failure reporting and region-pinned residency |
| D13 | Auth | O6.3 | OIDC + SAML + SCIM + MFA + ABAC + LDAP — a superset of what we can *see*; their federation is closed-source, so verify against the paid product before claiming |
| D14 | Compliance | O6.5 | Same certifications **plus** tamper-evident audit and a provable tenant-isolation ratchet as evidence |
| D15 | Proven scale | O0.3 + O1 | A published, reproducible ceiling with the harness in-repo — verifiable, not a marketing number |
| D16 | Incident intelligence | O7.5 | Contested, not won. The delta is **governance** — approval gate, risk-tier authz, SLO verification, auto-revert — plus measured RCA. Not "they have nothing" |
| D17 | SLOs | — | **We are behind.** They have backfill + reconciliation + budget charges. Either close it or stop selling SLOs as a strength |
| D17b, D18–D21 | Deploy intelligence, profiling, catalog, topology, deletability, audit, discipline | already | Confirmed wins; widened by O7.6, O7.7 |

**Two honest exceptions** (say so out loud rather than bluffing):

1. **D14 certifications are time-bound, not effort-bound.** SOC 2 Type II needs an
   observation window. Earliest credible date is ~9 months after O6.5 starts.
   Start it in parallel with O1, not after O6.
2. **D15 "2+ PB/day" is a customer-scale artifact.** We can publish a reproducible
   per-node ceiling and a linear-scaling proof; we cannot manufacture a petabyte
   reference customer. Compete on *reproducibility* of the number instead.

---

## 6. Sequencing

| Wave | Theme | Gate to exit |
| :---: | --- | --- |
| **O0** | Measure | `BENCHMARK.md` generated; README diagram corrected; throughput ceiling published |
| **O1** | Deployability + economics | `docker run` → data in <60 s; bytes/GB ≤ OpenObserve at ≤1.2× query p95 |
| **O2** | Query power | SQL + PromQL shipped; Grafana points at us; zero cross-tenant SQL escapes in the security suite |
| **O3** | Dashboards + reports | 20 panel types; Grafana import ≥80%; scheduled PDF delivered |
| **O4** | Streams + pipelines | Self-serve retention; pipeline with live preview; configurable SDR replaces the regex middleware |
| **O5** | Ingestion breadth | ≥12 sources, each e2e-proven |
| **O6** | Enterprise + scale | Federated query across 2 clusters; LDAP; BYOK; SOC 2 observation window open |
| **O7** | Moat | Every row of §5 demonstrable in a live demo |

**Recommended first six slices**, in order — highest evaluation-impact per unit of
effort:

~~1. **O0.1 + O0.2** — the benchmark harness.~~ ✅ done — and it rewrote items 3
and 4 below, which is what it was for.
~~2. **O0.4** — fix the README diagram.~~ ✅ done.

3. **O1.2's dedup slice** — *promoted to first*. Bounded Kafka retention plus
   split compaction is a config-and-worker change against the **largest measured
   gap** (11.2× on disk), and it pays down ingest throughput in the same stroke.
   Nothing else in this plan has that ratio of effort to measured effect.
4. **O1.1** — single-container mode. This is the one that changes the evaluation
   outcome most, and it is the hardest; start it early and let it run long. Now
   quantified: 23 containers and 5.00 GiB against 2 and 733 MiB.
5. **O2.1** — the SQL surface. Unblocks O3 entirely; nothing else raises the
   capability ceiling as much, and it is the **only** thing that closes the 2
   unaskable query classes.
6. **O3.1** — dashboards. The missing daily habit.
7. **O5.2** — Prometheus `remote_write`. Cheapest large migration wedge in the plan.

Waves O1 and O2 can run in parallel by different tracks; O3 depends on O2.1;
O4.3 depends on O4.2; O6.5 (compliance) should start on day one because it is
calendar-bound.

---

## 7. Risks

- **O1.1 is a re-architecture wearing a costume.** Four ports (bus, db dialect,
  graph store, blob) touch every service. Mitigation: land the ports one at a time
  behind interfaces with the cluster path as the default implementation, so
  cluster mode is never destabilized while lite is built.
- **O2.1 is the highest-severity security surface we will ever add.** A SQL
  endpoint on a multi-tenant store is exactly how tenant isolation dies.
  Mitigation: AST-level tenant injection (never string concatenation), reuse the
  F0.3 fail-closed guard, extend `TestNoRawTenantTableReads` to cover it, and a
  dedicated `/security-review` before merge. If it cannot be made provably safe,
  ship it single-tenant/on-prem only and say so.
- **VRL compatibility is a promise that is easy to half-keep.** A "compatible
  subset" that silently mis-evaluates an OpenObserve snippet is worse than no
  compatibility. Mitigation: publish the supported function list, and make
  unsupported constructs a **hard compile error**, never a silent no-op.
- **Scope: this plan is roughly the size of ROAD_TO_100 again.** Mitigation: the
  parity gate and the per-slice green-gates already in place; do not start a wave
  before its predecessor's exit gate is green.
- **Chasing their roadmap.** Waves O1–O5 are catch-up. If O7 keeps getting
  deferred, we win a substrate race against a Rust team with a head start and lose
  the intelligence lead that is actually defensible. Mitigation: **one O7 epic
  ships in parallel with every catch-up wave.**

---

### One-line summary

Measure first (**O0**), then buy back the evaluation with a single-container,
object-store-primary substrate (**O1**), a real query language (**O2**), and
dashboards (**O3**) — then take the ingest path (**O4–O5**) and the enterprise
floor (**O6**), while shipping one moat epic (**O7**) alongside each wave so the
incident-intelligence lead we already hold keeps widening.
