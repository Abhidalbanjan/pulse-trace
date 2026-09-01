# PulseTrace — Implementation Plan to Lead OpenObserve

_v2 · 2026-08-13 · supersedes the pre-verification draft (`git show HEAD:docs/COMPETITIVE_OPENOBSERVE_IMPLEMENTATION.md`)_
Spec: [COMPETITIVE_OPENOBSERVE.md](COMPETITIVE_OPENOBSERVE.md) · Evidence:
[COMPETITIVE_OPENOBSERVE_VERIFICATION.md](COMPETITIVE_OPENOBSERVE_VERIFICATION.md)

Written against a **source-verified** picture of OpenObserve (1,419 Rust files /
576k LOC, read structurally with citations). v1 was written from five web pages
and was wrong *in our favour* on three dimensions. This plan assumes the
corrected picture, which is less comfortable and considerably more useful.

**Standing rule: no stubs.** A slice is done when it is tenant-safe, observable,
load-tested, documented, reversible, and usable from the UI. "Backend merged, UI
next sprint" is not a slice — the parity gate rejects it and so does this plan.

---

## 1. Thesis

OpenObserve is no longer "a cheap log tool." Verified, they ship SLOs with
backfill and reconciliation, synthetics, 2,283 lines of anomaly detection, an
810-line workflow engine, an MCP tool server and LLM evaluations — on top of a
genuinely better substrate: single binary, Parquet + tantivy on object store,
DataFusion SQL, a full PromQL engine, real VRL pipelines, 21-panel dashboards.

We cannot win by racing them feature-for-feature on that substrate. They have a
head start, a faster language for the workload, and a team pointed at it.
**We win by being a different kind of product on a substrate that is merely equal.**

> **PulseTrace is a closed-loop reliability system.** Every signal lands in one
> causal model of the system; the platform detects, explains, acts, and
> **verifies its own action** — under governance a regulated buyer can audit.

Their architecture is *stream-centric*: independent streams you query and chart,
with features attached alongside. Ours is *entity-centric*: services,
dependencies, deploys, incidents and SLOs are first-class objects that signals
attach to. That is why they can add a chart type faster than us, and why we can
answer "what changed, what broke because of it, and what should I do" better than
they can — and why closing that gap costs them a re-architecture, not a sprint.

Three properties follow. Every phase serves at least one:

1. **Correlation is structural, not a feature.** One entity graph, one time
   model, one identity for a service across logs/traces/metrics/profiles/RUM/
   deploys. → P1, P3, P8, P9
2. **The loop closes.** Detect → explain → propose → govern → act → **verify** →
   revert if wrong. Nobody in this market closes the last two steps. → P9
3. **Governance is a feature, not paperwork.** Per-record erasure, tamper-evident
   audit, approval gates with risk tiers. Their data is immutable by design; this
   is the one place they are architecturally stuck. → P5, P9, P10

### 1.1 Parity buys nothing — but its absence loses everything

Deployability, storage economics, ad-hoc query, dashboards, streams, pipelines,
ingestion breadth (P1–P7). These win no deals. Their absence loses deals in the
first twenty minutes, before anyone sees what we are actually good at.

### 1.2 Where we must exceed, and how

| Where | Their verified position | How we pass them |
| --- | --- | --- |
| SLOs | **Ahead of us** — backfill, reconcile, budget charges | Reach their floor, then bind SLOs to the entity graph and the deploy gate: budget burn attributed to a **change**, not merely observed |
| Anomaly detection | 2,283 LOC, shipped | Ours must be *causal* — name a **suspect entity** via graph walk, not flag a series |
| Automation | Workflow engine + action scripts | Governance + **verification/auto-revert**: automation that proves it worked |
| Profiling | None (their flame graph renders trace spans) | Continuous profiling wired into the same entity and incident timeline |
| Deletability | Architecturally blocked | Per-record erasure with a signed certificate |
| Audit | 105-line publisher | Hash-chained, verifiable, exportable |

---

## 2. Phase map

| # | Phase | Outcome | Slices | Eng-wks |
| :---: | --- | --- | :---: | :---: |
| **P0** | Ground truth | Measured baseline vs OpenObserve; no unverified claim survives | 4 | 2 |
| **P1** | Core runtime | Five ports; one binary; `docker run` → data in <60 s | 7 | 9 |
| **P2** | Storage engine | Dedup the ingest path, compact splits, object-store-primary, cost accounting | 6 | 6 |
| **P3** | Query engine | Federated SQL (DuckDB) + PromQL + cross-signal joins | 7 | 11 |
| **P4** | Streams & schema | Registry, inference, 15-field settings, self-serve retention | 4 | 5 |
| **P5** | Pipelines & governance | Real VRL, enrichment, SDR, **per-record erasure** | 6 | 9 |
| **P6** | Dashboards & reports | 24 panels, variables, annotations, importers, reports | 6 | 9 |
| **P7** | Ingestion & migration | `_bulk`, remote-write, Kinesis, Loki, **syslog**; keep Datadog | 7 | 5 |
| **P8** | Reliability intelligence | SLO v2 (passes theirs), causal anomalies, forecasting | 6 | 9 |
| **P9** | Closed-loop autonomics | Entity spine, causal RCA v2, governance, **verify + revert** | 7 | 12 |
| **P10** | Enterprise & scale | Federation, workload mgmt, LDAP, BYOK, compliance | 6 | 10 |

Estimates assume 2–3 engineers and are **re-forecast after P1** — the first phase
touching every service, and therefore the only honest calibration point.

### 2.1 Critical path

```
P0 ──► P1 ──┬──► P2 ──► P4 ──► P5
            │           └────► P8   (retention + stream windows feed SLOs)
            └──► P3 ──┬──► P6
                      └────► P9   (cross-signal joins are the RCA substrate)
P7  — independent after P1 (the ingestproxy pattern exists today)
P10 — P10.5 (compliance) starts week one; the rest follows P2/P3
P9  — the moat: one slice ships alongside every phase from P2 onward
```

**Track A** platform: P1 → P2 → P4 → P5 · **Track B** query/UX: P3 → P6 ·
**Track C** ingestion: P7 · **Track D** intelligence: P8 → P9 ·
**Track E** compliance calendar: P10.5 from week one.

**Non-negotiable:** P9 interleaves. If the moat waits until ten phases of
catch-up are done, we will have spent a year becoming a worse OpenObserve.

### 2.2 Migration ledger

Next free today: **gateway 026 · correlation 007 · alert 003**.

| # | Service | Slice | Tables |
| --- | --- | --- | --- |
| 026 | gateway | P2.3 | `storage_objects`, `storage_accounting` |
| 027 | gateway | P3.2 | `saved_queries`, `query_budgets`, `query_audit` |
| 028 | gateway | P4.1 | `streams`, `stream_settings`, `stream_schema_fields` |
| 029 | gateway | P5.1 | `pipelines`, `pipeline_versions`, `pipeline_node_stats` |
| 030 | gateway | P5.3 | `enrichment_tables`, `enrichment_rows` |
| 031 | gateway | P5.4 | `redaction_rules`, `redaction_events` |
| 032 | gateway | P5.5 | `erasure_requests`, `erasure_certificates` |
| 033 | gateway | P6.1 | `dashboard_folders`, `dashboards`, `dashboard_versions` |
| 034 | gateway | P6.5 | `dashboard_reports`, `report_runs` |
| 035 | gateway | P10.2 | `workload_policies`, `query_queue_stats` |
| 036 | gateway | P10.4 | `tenant_keks`, `kek_rotations` |
| 037 | gateway | P10.1 | `peer_clusters`, `cluster_health` |
| 007 | correlation | P8.1 | `slo_v2`, `slo_windows`, `slo_budget_ledger` |
| 008 | correlation | P8.2 | `slo_backfill_jobs`, `slo_reconcile_log` |
| 009 | correlation | P8.4 | `anomaly_findings`, `anomaly_feedback` |
| 010 | correlation | P9.1 | `entities`, `entity_edges`, `entity_aliases` |
| 011 | correlation | P9.5 | `remediation_verifications`, `revert_log` |
| 012 | correlation | P9.6 | `llm_spans`, `llm_cost_attribution` |

### 2.3 Definition of Done — the anti-stub contract

Every slice, all nine. No exceptions, no "follow-up ticket."

| | Bar |
| --- | --- |
| **1 Functional** | Every path works end-to-end. No `TODO`, no dead control, no "coming soon." |
| **2 Parity** | Backend capability has a UI control; UI control hits a real route; or registered `uiNone`. CI-enforced. |
| **3 Tenant-safe** | Tenant resolved server-side; the store guard applies; a cross-tenant test proves denial. |
| **4 Tested** | Pure logic table-driven · integration against the real datastore · one Playwright e2e per user flow · **one failure-injection test per external dependency**. |
| **5 Performance** | A stated budget (§3), measured, wired into k6 or the bench harness so regression fails CI. |
| **6 Observable** | Emits RED metrics + structured logs + spans. A dashboard panel exists for it. |
| **7 Operable** | Runbook entry: failure modes, detection, recovery. Degrades explicitly, never silently. |
| **8 Reversible** | Feature-flagged; backout documented; migration forward-compatible with the previous release. |
| **9 Documented** | User docs + API reference updated in the same commit. |

---

## P0 — Ground truth · 4 slices · 2 weeks

**Thesis.** We are about to spend a year on comparative claims, none of which is
currently measured. P0 is cheap and makes every later phase falsifiable.
**Non-goals.** Tuning. P0 measures; it does not optimise.

> **Status: P0.1 and P0.2 are delivered.** The harness lives in
> `scripts/bench/`, results in [BENCHMARK.md](../BENCHMARK.md), and the exit
> condition below is met — §1 of the spec doc now cites measured numbers.
> It paid for itself immediately: the measurement **rescoped P2** (the gap is
> duplication, not format — see the note there) and **confirmed P3 is the only
> phase that closes D3**. It also found a shipped bug no test covered: the regex
> class failed 20/20 because Quickwit 0.8 has no regex and parsed our `field:/…/`
> as a wildcard. Fixed; the class now runs (79/126 ms against their 47/65 ms —
> slower than theirs, but it used to be impossible).
> **Still outstanding: P0.3, P0.4, and cold-start-to-first-query** — the one
> sample in P0.2's list the harness does not yet collect.

**P0.1 · Comparative harness · S**
`scripts/bench/compose.openobserve.yml` (pinned tag, MinIO backing, resource
limits matched to ours) · `corpus/gen.go` — seeded, byte-reproducible: 10 GB
mixed (70% logs incl. unstructured syslog, 20% OTLP traces, 10% metrics),
cardinality `service×40 · pod×2000 · customer_id×50k` · `load.sh` replays into
either target natively. **Test:** same seed ⇒ identical corpus SHA-256.

**P0.2 · Six-class query suite + `BENCHMARK.md` · M**
Classes: needle-in-haystack · full-scan aggregation · high-cardinality group-by ·
trace-by-id · metric range · regex scan. Samples: bytes-on-disk per GB ingested,
throughput ceiling, p50/p95/p99 per class, cold-start seconds, container count,
idle and loaded RSS. Results block **machine-written**, same discipline as
`PERF_BASELINE.md`. Publishes losses as prominently as wins.

**P0.3 · Our real throughput ceiling · S**
`CEILING=1` on `scripts/load/run-baseline.sh`: dedicated tenant, limiter raised,
ramp to the knee, record **the binding constraint** — Kafka lag / ClickHouse
active parts / gateway CPU (the sampler already collects all three).

**P0.4 · Publish the entity model as an ADR · S**
P9.1 is the architectural bet; every later phase keys to its vocabulary. Write it
down before three phases invent three different spellings of "service."

**Exit:** `BENCHMARK.md` regenerates with one command; §1 of the spec doc cites
measured numbers instead of their marketing.

---

## P1 — Core runtime · 7 slices · 9 weeks

**Thesis.** **23 containers — measured, against their 2** — is the largest
adoption barrier and a real cost line: **5.00 GiB resident against their
733 MiB**. Five ports let the same code be a one-container product or the
cluster we run today.

**Non-goals.** Rewriting business logic.

**Key decision — strangler, not big-bang.** Each port merges independently behind
an interface, with both implementations passing one **conformance suite**;
cluster stays the default until P1.7, so cluster behaviour cannot regress and the
existing test suite is the acceptance proof.
_Rejected:_ a `lite` fork — guarantees divergence, doubles the test matrix.

### P1.1 — `shared/bus` port + Kafka adapter · M
```go
package bus

type Handler func(ctx context.Context, m Message) error

type Bus interface {
    Publish(ctx context.Context, topic, key string, value []byte) error
    PublishBatch(ctx context.Context, topic string, entries []*models.LogEntry) error
    Subscribe(ctx context.Context, group string, topics []string, h Handler) error
    Close() error
}

type Message struct {
    Topic, Key string
    Value      []byte
    Timestamp  time.Time
    Headers    map[string][]byte // W3C traceparent survives the abstraction
}
```
`kafka_adapter.go` wraps today's `Producer`/`ConsumerGroup` verbatim; sarama stops
leaking past this file, and trace-header propagation moves here so both adapters
inherit it. **Call sites:** `log-service`, `alert-service`, `correlation-service`,
`topology-service`, `gateway/internal/logbridge`, `gateway/internal/ingestproxy`.
**Failure mode:** broker unreachable → bounded retry with jitter → typed
`ErrBusUnavailable` the caller surfaces. Never a silent drop.
**DoD:** existing Kafka integration tests pass **untouched**.

### P1.2 — In-process bus with a durable WAL · L
The hard slice. An in-memory channel is not a Kafka replacement — a lite user
losing their queue on restart is a data-loss bug we shipped on purpose.

- Per-topic segmented WAL at `$DATA_DIR/bus/<topic>/<seq>.seg`, 64 MB segments,
  `fsync` on rotation and every 100 ms (configurable — the same durability window
  as our current Kafka acks).
- Consumer groups are offset files with atomic rename; at-least-once delivery,
  replay from last commit on restart.
- **Back-pressure:** ring full + spill budget exhausted → `Publish` blocks with a
  deadline, then returns `ErrBusFull`; the gateway maps that to HTTP 429 with
  `Retry-After`. Silently dropping telemetry corrupts every downstream count and
  is never acceptable.
- **Tested:** segment rotation · offset commit/replay · truncated-tail recovery
  after `kill -9` · redelivery semantics · per-topic ordering under concurrent
  producers · the back-pressure boundary.
- **Conformance suite** (`shared/bus/conformance_test.go`) runs identical
  assertions against Kafka *and* in-process. This is what keeps lite and cluster
  semantically identical for the life of the product.
- **Budget:** ≥50k msg/s publish; p99 < 5 ms at 20k msg/s.

### P1.3 — `shared/db` dialect layer · M
`Open(ctx) (*sql.DB, Dialect, error)` by URL scheme. Rewrites: `JSONB`→`TEXT`
(+JSON1), `BIGSERIAL`→`INTEGER PRIMARY KEY AUTOINCREMENT`, `NOW()`→
`CURRENT_TIMESTAMP`, `$n`→`?`, `ON CONFLICT` variants.

⚠ **The audit-chain hazard.** `WriteAudit` serialises on
`pg_advisory_xact_lock` so concurrent writers cannot fork the hash chain (F20).
SQLite has no advisory locks. Implementation: a `chain_lock(name TEXT PRIMARY KEY)`
row taken under `BEGIN IMMEDIATE`, giving the same single-writer guarantee.
**This slice does not merge without a concurrency test that demonstrably forks
the chain on a naive implementation and passes on ours.**

**Golden test:** all 25 gateway + 6 correlation migrations applied to SQLite, the
resulting schema diffed against Postgres (names, types, nullability, indexes). An
untranslatable migration gets a `NNN_name.sqlite.sql` sibling — never degrade the
Postgres path to fit SQLite.

### P1.4 — `GraphStore` port + SQL adapter · M ✅ *shipped*
Neo4j appears in only four files and the Cypher is shallow.
```go
type GraphStore interface {
    UpsertService(ctx context.Context, tenant string, s Service) error
    UpsertEdge(ctx context.Context, tenant, from, to string, m EdgeMeta) error
    Graph(ctx context.Context, tenant string, f Filter) (Graph, error)
    Walk(ctx context.Context, tenant, root string, d Direction, maxDepth int) ([]Path, error)
    Catalog(ctx context.Context, tenant string) ([]CatalogEntry, error)
    DeleteTenant(ctx context.Context, tenant string) error
}
```
SQL adapter: `graph_nodes`/`graph_edges`, walks as recursive CTEs with the same
depth cap and cycle handling as the Cypher. `tenantdata/purge.go` moves onto the
port so tenant deletion keeps working in lite.
**Equivalence test:** the same fixture graph through both implementations yields
identical path sets — cycles and diamond dependencies included.

> **Status: delivered, with three corrections to this slice as originally
> written.** The port is `shared/graph`, the adapters are
> `shared/graph/sqlstore` and `shared/graph/neo4jstore`, and the gate is a
> twelve-assertion conformance suite run against both backends — confirmed able
> to fail before being trusted, by breaking each implementation in turn and
> checking that only its own subtests went red.
>
> **1. The interface above has six methods; the Cypher has ten.**
> `UpsertServiceCatalog`, `UpsertServiceMetadata`, `UpdateCausalPath` and
> `GetServiceState` are all graph-backed and all had to come across. The sketch
> was a design; the port is the surface that exists.
>
> **2. `Walk` was not built, and the equivalence test as specified cannot be
> met.** There is nothing to be equivalent *to*: every Cypher statement in the
> service is depth-1 or a whole-tenant scan, and no traversal, depth cap or
> cycle handling exists to compare against. Building one would mean writing a
> Cypher walk purely so the SQL walk could be measured against it. The
> conformance suite replaces the equivalence test — same purpose, two
> implementations forced to agree, but over behaviour that exists. When
> something needs multi-hop it gets its own slice; `sqlstore` documents where
> the recursive CTE goes.
>
> **3. `tenantdata/purge.go` did not need to move.** It never talked to Neo4j —
> it issues an HTTP `DELETE` to topology-service's `/api/v1/topology/tenant`,
> which is already the right seam and works whatever backend is behind it. Only
> a result label naming the wrong backend was wrong. (Relatedly: Neo4j appears
> in eight files, not four, though four are trivial wiring.)
>
> **This slice does not remove a container, and the text above reads as though
> it does.** `Neo4jRepository` was two stores wearing one name: the graph, and a
> Redis half doing read-through caching, span buffering and edge metrics. Only
> the graph moved, so lite still requires Redis — that decision belongs to P1.5
> and is not made here. The SQL path is also Postgres-only until `migrate.Run`
> learns to select `.sqlite.sql` siblings, which is **P1.6's outstanding
> piece** and currently blocks lite from running the topology on SQLite.

### P1.5 — `shared/blob` + `shared/analytics` ports · M
`blob`: Put/Get/List/Delete/Stat over S3, Azure, GCS, local FS; credential
resolution centralised. Consumers: bus spill, compactor output, report PDFs,
enrichment tables.
`analytics`: the seam the ClickHouse handlers already funnel through.
**Critical:** `assertTenantScoped` / `referencesTenantScopedTable` run on *every*
implementation, and `TestNoRawTenantTableReads` ratchets all of them. A backend
that bypasses the guard is a build failure.

### P1.6 — Single binary with roles · L
`pulsetrace --roles=all|gateway|ingest|query|worker|notify`. Each service exposes
`RegisterRoutes(mux)` + `StartWorkers(ctx, deps)` — correlation and topology
already do. `shared/runtime.ResolveMode(env)` resolves `lite|cluster` **once** at
startup; contradictory config (lite + `KAFKA_BROKERS`) is a **startup error** with
a specific message, never a silent preference. Tested across every valid role
combination and every contradictory env pairing.

> **Carried in from P1.3 and P1.4, and blocking lite until it is done.**
> `shared/migrate.Run` skips `*.sqlite.sql` siblings but nothing selects them,
> so the hand-written SQLite rendering of a migration is currently unreachable
> outside the golden schema test. Choosing the right sibling needs the code that
> knows which backend it is, which is this slice. Until then every SQL path
> added by P1.4 onwards is Postgres-only, and P1.4's own
> `TOPOLOGY_STORE` switch is a local stand-in for `ResolveMode` that should be
> deleted here rather than left as a second way to decide the same thing.

### P1.7 — Lite image, first run, and the 60-second proof · M
`Dockerfile.lite`: binary + bundled Quickwit + built frontend, `$DATA_DIR` the
only volume. First-run bootstrap creates the `default` tenant and an admin user
with a generated password printed once to stdout, seeds the index, runs
migrations — zero manual steps between `docker run` and a login screen.
Cluster-only features render an explicit "requires cluster mode" state, never a
dead control. `scripts/lite-to-cluster.sh` migrates SQLite→Postgres through the
dialect layer, drains bus spill into Kafka, copies the Quickwit index (format
identical by design).
**e2e:** clean volume → login → OTLP log → visible in Explorer, **asserting
wall-clock < 60 s**; plus an upgrade test proving the same query returns the same
rows after migrating to cluster.

**Exit:** `docker run -p 8080:8080 pulsetrace/pulsetrace` yields a working product
in under a minute; the cluster path is unchanged; all pre-existing gates green.

---

## P2 — Storage engine · 6 slices · 6 weeks

> **Rescoped after P0 measured it.** [BENCHMARK.md](../BENCHMARK.md): we write
> **3.39 GiB per GiB ingested against their 309 MiB — 11.2×**. The measurement
> also found the *cause*, which is what changes this phase: it is **duplication,
> not format**. (Two earlier runs reported 8.4× and 7.5×; both under-measured
> *us*, because the sampler skipped anonymous volumes. The gap was always this
> size.)

**Thesis.** The substrate is not the problem. Our log tier is Quickwit, which
*is* tantivy — the same engine family as their Parquet + tantivy/puffin — so the
11.2× is not columnar beating an inverted index. It decomposes into three things
we own:

1. **Kafka retains a full second copy** of every log record after Quickwit has
   indexed it. Pure duplication, and the same hop that makes our ingest 572 s
   against their 74 s. Highest leverage item in the phase.
2. **Quickwit splits are never compacted.** Small splits carry per-split
   overhead and never merge, so the index grows superlinearly in file count.
3. **Traces/metrics/RUM sit on SSD-primary ClickHouse** for the whole retention
   window, with no compactor and no file-list index — which is how they enforce
   retention and account for cost.

**Key decision — do not write a storage engine, now with evidence.**
_Rejected:_ building our own Parquet+index engine. Twelve-plus months, it is
their core competency, and P0 confirmed Quickwit already gives us the property
that matters — the format is not where the bytes go. We close the gap with
retention policy, compaction, tiering and accounting. **The rewrite risk this
phase was carrying is retired.**

**P2.0 · Ingest-path deduplication · S — do this first** ✅ **shipped** — the
cheapest large win in the plan. Kafka's `logs` topic kept its full 168h default
after Quickwit had durably indexed the record, so the corpus existed twice.
Retention is now 24h: enough to survive an overnight failure found the next
morning, and a config knob because P2.4's rebuild-from-replay cannot reach
further back than this value.

**What made it non-trivial.** Lowering `retention.hours` alone reclaims
*nothing*. Kafka deletes only **closed** segments, and the defaults are a 1 GiB
segment rolling after 168h — a 2 GiB corpus over 10 partitions puts ~215 MiB in
each, a fifth of one segment, so every partition holds one perpetually-open
segment and nothing is ever collectable. Confirmed on the live broker before the
change: 10 partitions, 10 segment files, **0 closed**. The fix is
`segment.bytes` 128 MiB + `roll.hours` 6 (so segments close at all), plus
`index.size.max.bytes` 2 MiB (10 MiB is preallocated per *open* segment and
sized for a 1 GiB one). `scripts/bench/verify-kafka-retention.sh` asserts the
structural property and fails on the pre-fix broker.

**Verified on a live stack.** Same corpus, same 10 partitions, before and
after:

| | segment files | closed / collectable |
| --- | :---: | :---: |
| Before (168h, 1 GiB segments) | 10 | **0** |
| After (24h, 128 MiB segments, 6h roll) | 28 | **18, across all 10 partitions** |

Zero collectable segments is the pathology; 18 is retention becoming able to do
its job. The watchdog was also exercised against the live broker and discovered
all three consumer groups — including Quickwit's
`quickwit-pulsetrace-logs:01M09PSPN3891DCRKAV6F6WF30-kafka-logs-source`, which is
the case a hardcoded group list would have missed.

**Correction to this slice as originally written.** It said "verify by re-running
the harness, not by reasoning." That is wrong, and the harness cannot settle it:
footprint is sampled immediately after a burst ingest, and with a 24h window
nothing ages out inside a ten-minute run. The benchmark will show the segment
and index savings only. What the slice actually buys is the elimination of
**unbounded growth** — under the old config each benchmark run accumulated
(2.22 GiB after one load, 4.32 GiB after two, never released), whereas the
deployment now plateaus at a 24h working set. That is a steady-state property,
so it is verified structurally and by the watchdog, not by a burst measurement.
**A slice whose benefit the harness cannot see needs its own evidence, and
saying so is cheaper than publishing a number the method does not support.**

**Safety, and why it is part of this slice.** At 168h, consumer lag was an
efficiency question; at 24h a consumer stalled overnight loses records silently,
and `logs` has three independent consumer groups. `shared/kafka` now carries a
retention watchdog tracking each group's committed offset against the **oldest
offset still retained** — lag alone is the wrong signal, since it says nothing
about whether the unread records still exist. Groups are discovered per sample,
because Quickwit's group ID embeds a generated ULID.
**Budget:** Kafka disk bounded by the retention window rather than growing
monotonically, with **zero** loss of indexed records.

**P2.1 · Object-store-primary tiering · M** — hot window in **hours**
(`TTL … + INTERVAL 6 HOUR TO DISK 's3'`), hot volume sized for the window. The
hardcoded `INTERVAL 7 DAY` in `rum_handler.go` / `synthetics_handler.go` becomes a
parameter defaulted to today's value; the retention decision moves to P4. Confirm
via P0 whether collector-written `otel_logs` duplicates what Quickwit indexes — if
so (likely; nothing queries it) stop writing it: pure cost deletion.
**Budget:** query p95 within 1.2× of the SSD-primary baseline.

**P2.2 · Compactor · L** — `gateway-service/internal/compactor/`:
`merge.go` (small parts → 2 GB target, never the active partition, bounded
concurrency so merges cannot starve queries) · `retention.go` (drops partitions
past per-stream retention, Quickwit splits and ClickHouse partitions alike) ·
`filelist.go` (parts/splits per tenant per stream in Postgres, so accounting is a
lookup not a scan).
**Pure logic:** `selectMergeCandidates(parts, policy)`; `partitionsToDrop(...)` —
an off-by-one here is silent data loss, so it is table-tested at every boundary,
DST included.
**Safety:** ships in `dry-run` for one full retention cycle, logging what it
*would* drop, before enforcement. Non-negotiable.
**Observability:** backlog depth, bytes merged, partitions dropped, merge-duration
histogram; alert on backlog growth.

**P2.3 · Storage accounting & cost attribution · M** — migration **026**.
`GET /api/v1/storage/usage?group_by=stream|tenant|tier|team`; projected monthly
cost from configurable per-tier rates; compaction backlog. **UI:** Settings →
Storage — bytes by tier/stream/team, projected spend, top-10 most expensive
streams. Foundation of the cost-intelligence differentiator: they compete on being
cheap, we also explain the bill.

**P2.4 · Quickwit lifecycle ownership · M** — today it is configured once by an
init container and never managed, which is cause (2) above: **splits are never
compacted.** Bring it under lifecycle: an explicit split merge policy, retention
via the compactor, index health in Settings, and an admin action to rebuild an
index from Kafka replay (note the coupling to P2.0 — replay cannot reach further
back than Kafka retention, so the two slices must agree on the margin).
Existing deployments also need the index recreated to pick up
`record: position`, so fold that migration in here rather than shipping a second
disruptive index change later.

**P2.5 · Cost benchmark · S** — re-run `run-benchmark.sh`. The baseline is no
longer hypothetical: **3.39 GiB/GiB, 6.78 GiB on disk, 572 s ingest, 23
containers, 5.00 GiB RSS**, against their 309 MiB/GiB, 618 MiB, 74 s, 2, 733 MiB.
**Exit gate: bytes-on-disk per GiB ingested ≤ theirs, with query p95 ≤ 1.2× of
the P0 baseline** — and the four expressible query classes must not regress from
their measured values (70/102, 13/27, 79/126, 43/64 ms p50/p95). Only
trace-by-ID is a durable win; the other three sit inside run-to-run variance, so
gate on *no regression*, not on holding a lead we cannot reproduce.

---

## P3 — Query engine · 7 slices · 11 weeks

**Thesis.** No user query language is our hardest ceiling: every question a user
can ask must have been anticipated by a Go handler. They have DataFusion SQL and a
full PromQL engine. We need both — plus the thing neither they nor Datadog can do:
**joins across signal types**.

**Key decision — embed DuckDB as the federation engine.** `go-duckdb` executes
ANSI SQL (windows, CTEs, joins) over Arrow batches fed by per-store scanners.
_Rejected:_ hand-rolling a planner/executor (a year, badly); pushing everything
into ClickHouse (cannot reach Quickwit or Postgres, so no cross-signal joins).
DuckDB doubles as lite mode's analytics engine — one decision solves two phases.

**Key decision — physical tenant isolation, not predicate injection.** Scanners
are constructed *with* a tenant ID and cannot emit another tenant's rows, because
the underlying store query is built by our code, not the user's. AST rewriting is
the second layer, the store guard the third. A total SQL-parser compromise still
cannot cross tenants — the data never enters the engine. That is the difference
between "we validate the query" and "the attack is inexpressible."

### P3.1 — Query core: parse, plan, isolate · L — *security-critical*

> **In progress.** `gateway-service/internal/sqlq/` now carries the catalog,
> policy, escape suite, budget, scanner interface and a working DuckDB engine —
> 47 tests plus a live-store integration suite. The two benchmark classes that
> were *not expressible* now **parse, plan and execute** — but see the scale
> limit below before treating D3 as closed.
>
> **D3 is closed — by push-down, not by local execution.** The first attempt was
> not enough and the plan said so: local execution has to fetch every row an
> aggregate covers, and Quickwit caps a search at `max_hits = 10_000`, so
> `SELECT count(*) FROM logs` over the 5.1M corpus could not be answered *at
> all*. The class was expressible and still not answerable.
>
> The fix was to stop moving rows. Quickwit returns an exact `num_hits` for a
> search with `max_hits: 0` and supports terms aggregations; ClickHouse and
> Postgres are SQL. Measured against the running stack:
>
> | | result | time | rows moved |
> | --- | --- | --- | --- |
> | `count(*)` over the whole corpus | 5,104,773 | 15 ms | **0** |
> | top-10 `GROUP BY service_name` | 10 buckets over 1,280,444 rows | 25 ms | **0** |
>
> For reference the benchmark measured OpenObserve at 71 ms and 118 ms p50 on
> the same two classes — **but these numbers are not comparable**: theirs came
> from the harness under matched resource caps, these are ad-hoc against a dev
> stack. Re-running the harness is what would settle it, and until that happens
> the honest claim is "answerable and fast", not "faster than theirs".
>
> **Push-down does not weaken the isolation argument.** No user SQL is sent
> anywhere: a recognised shape in the validated AST becomes a *typed method
> call* — `CountAll`, `GroupCount` — on the scanner, which builds its own
> statement with the tenant bound exactly as a row scan does. The grouped column
> is re-checked against the catalog before it is named to a store.
>
> **The matcher is deliberately narrow**, because this is the one place where a
> bug returns a *wrong number* rather than an error. Six shapes push down;
> eleven that would change the answer are refused and fall back to local
> execution — `WHERE`, `HAVING`, joins, `count(DISTINCT)`, `count(col)`,
> ascending order, `OFFSET`, and set operations among them. Both directions are
> table-tested.
>
> The guarantee for everything still executed locally is unchanged and asserted
> by a test: at a scale it cannot serve, the engine **fails loudly**. An
> aggregate over a silently truncated sample would be strictly worse than an
> error, because a wrong count looks exactly like a right one.
>
> **The catalog was rewritten against the live stores.** The first draft invented
> columns. Reality: `otel_traces` has **no TenantID column** — the tenant lives
> in `ResourceAttributes['tenant.id']`, so a shared "add WHERE TenantID" helper
> would have been silently wrong for the largest table; Quickwit's index has no
> `span_id`; and there is **no `otel_metrics` table at all**, only five typed
> ones. `metrics` is therefore *removed* from the catalog rather than shipped as
> a name that resolves and then fails — a modelling decision (which unifying
> schema? union across five shapes?) masquerading as a mapping.
>
> **Executor decision — DuckDB, local execution.** Scanners fetch tenant-bound
> rows; user SQL runs only against those, so it never reaches a store and
> cross-tenant access is unrepresentable rather than blocked. Two costs the plan
> had not accounted for, both measured:
>
> - **Alpine cannot host it.** go-duckdb links a prebuilt static library built
>   against glibc; on musl the link fails on `malloc_trim`, `backtrace_symbols`
>   and `__res_init`. gateway-service moves to `golang:1.26.6` +
>   `distroless/cc-debian12` with `CGO_ENABLED=1`. Every other service is
>   unchanged.
> - **Image size, which is dimension D1 — the one we already lose.** Measured on
>   linux: the gateway binary goes **33 MB → 100.6 MB** once DuckDB is linked
>   (104,772 symbols), taking the image from 68.2 MB to a measured **192 MB**.
>   distroless rather than debian-slim saves ~100 MB of that. An earlier note
>   here said +38 MB — that was measured on a darwin test binary and understated
>   it; the real cost is +67 MB. Paying for D3 with D1 is a real trade and it is
>   recorded rather than absorbed.
>
> **Builder and runtime must be the same Debian release.** `golang:1.26.6` is
> trixie (glibc 2.41) and emits a binary needing `GLIBC_2.38`;
> `distroless/cc-debian12` is bookworm (glibc 2.36). The image builds and links
> cleanly and then every container exits at startup. Pinned to
> `golang:1.26.6-bookworm`. The lesson generalises: **verifying that a cgo
> binary links, or running it inside the builder, does not verify that it runs
> in the runtime image** — only running it there does, and skipping that cost a
> CI cycle.
>
> **Two bugs only real stores could find**, both invisible to the unit suite:
> `ClickHouseScanner` sent no credentials, so every ClickHouse relation failed
> with `Code: 516 Authentication failed` while `httptest` — which does not check
> credentials — passed; and loading Postgres rows into DuckDB failed with
> `could not bind parameter`, because every column was declared VARCHAR while
> the driver returns `time.Time`, `int64` and `[]byte`. The second mattered
> beyond the crash: VARCHAR columns would have made `avg(duration_ms)`
> arithmetic over strings. Columns are now typed from the data.
>
> **A rendering bug the tests caught, worth keeping in mind for P3.2:**
> re-rendering the validated AST emitted MySQL charset introducers
> (`_UTF8MB4'x'`) that DuckDB rejects. The tree was faithful; the text was not.
> Any accepted query shape has to be exercised against the engine that runs it,
> not merely validated.

`gateway-service/internal/sqlq/`
- `parse.go` — a real SQL parser (`pingcap/parser`; rationale recorded in-slice).
- `policy.go` — `SELECT` only; no DDL/DML, no system schemas, no
  `file()`/`url()`/`remote()`/`s3()`, no `INTO OUTFILE`; bounded subquery depth
  and join count.
- `plan.go` — resolves refs against the virtual catalog, assigns each to a store,
  decides push-down vs local execution.
- `scan.go` — tenant-bound scanners: `LogScanner` → Quickwit ·
  `AnalyticsScanner` → ClickHouse through the existing `queryScoped` guard ·
  `MetaScanner` → Postgres. Each yields Arrow batches for exactly one tenant, by
  construction.
- `budget.go` — per-role scanned-rows / bytes / time-range / wall-clock caps.

**The escape suite is the deliverable:** comment injection · UNION to another
tenant · CTE aliasing a scoped table · correlated subquery · JOIN to a system
table · nested view · `FORMAT` abuse · stacked statements · unicode quoting.
Each asserts rejection *or* correct isolation, plus a fixture test proving tenant
A cannot retrieve tenant B's row through **any** accepted query.
**Gate:** `/security-review` before merge; `TestNoRawTenantTableReads` extended to
cover `sqlq`. If isolation cannot be *demonstrated*, this ships single-tenant /
on-prem only and the docs say so plainly.

### P3.2 — SQL endpoint · M ✅ *shipped*
`POST /api/v1/query/sql`, migration **027** (`query_audit`, `query_budgets`),
NDJSON streaming, cancellable via request context, every execution audited —
**including refusals**, because a run of rejections against system schemas is
the signal you most want and a success-only log discards it. Registered in the
parity registry as API-first; the workbench UI is P3.6.

**Honest limit:** the response streams, the *computation* does not. `sqlq`
materialises the result before returning, so this is not incremental execution
and the memory profile is that of the whole result set. Saying otherwise would
misrepresent it.

Migration **027**. `POST /api/v1/query/sql` — streaming NDJSON, cancellable via
request context, every execution recorded in `query_audit` (who, what, bytes
scanned, duration). Saved queries extend `saved_search_handler.go` rather than
duplicating it.

### P3.3 — Virtual catalog & cross-signal joins · L — *the differentiator*
One logical schema: `logs`, `traces`, `spans`, `metrics`, `rum`, `synthetics`,
`profiles`, `deploys`, `incidents`, `slos`, `entities`.
`GET /api/v1/query/schema` powers autocomplete. Cross-store joins execute as
bounded hash joins in the gateway; the smaller side must fit a row budget or the
query is **rejected with a clear message** — never silently truncated.

The query that sells the product:
```sql
SELECT d.version,
       count(*)                        AS errors,
       quantile(0.99, t.duration_ms)   AS p99
FROM logs l
JOIN traces  t ON l.trace_id = t.trace_id
JOIN deploys d ON d.service  = l.service
              AND l.timestamp BETWEEN d.started_at AND d.started_at + INTERVAL 1 HOUR
WHERE l.level = 'ERROR'
GROUP BY d.version
ORDER BY errors DESC
```
A stream-centric architecture cannot answer this without the user pre-joining the
data at ingest time.

### P3.4 — PromQL engine + Grafana datasource · L
`gateway-service/internal/promql/` — parser, evaluator, and the Prometheus HTTP
API under **`/api/v1/prom/*`** (⚠ `POST /api/v1/series` already exists as an
ingest route — do not overload it). Endpoints: `query`, `query_range`, `series`,
`labels`, `label/{name}/values`, `metadata`.
**Tested against the upstream PromQL test corpus subset** — `rate`, `increase`,
`histogram_quantile`, `sum by`, offset, `@`. Semantics must agree with the
existing `metricAggExpr` so the two engines never disagree. Integration test:
a real Grafana container renders a real dashboard against us.

### P3.5 — Query acceleration · M
Result + histogram cache in the **Redis already in compose and barely used**,
keyed `(tenant, normalized-sql, closed-time-bucket)`; the trailing open bucket is
never cached. `normalizeForCacheKey` is **tenant-sensitive** and fuzz-tested as
such — a key that ignores tenant is a cross-tenant leak. Time-partitioned
execution streams long ranges progressively, serving cached buckets instantly.
**Budget:** repeat-query p95 ≥5× faster; first byte < 500 ms on a 7-day range.

### P3.6 — Query workbench · M
`/query` — editor with schema autocomplete, result grid, **Visualize** (hands to
P6), save, share via URL, honest partial-result and budget-exceeded states.
Explorer and Metrics gain "Open in SQL" — the discoverability path that makes the
feature actually get used.

### P3.7 — Search-around, generalised · S
`GET /api/v1/{store}/{id}/context` — today's log-only surrounding-context
generalised to traces and RUM sessions.

---

## P4 — Streams & schema · 4 slices · 5 weeks

**Thesis.** Retention is compiled into DDL and nothing surfaces the schema
Quickwit already infers. A customer cannot say "keep audit 400 days, app logs 14."

**Key decision — target their verified field list**, not the four the docs
mentioned. `src/config/src/meta/stream.rs:941-971` carries fifteen.

**P4.1 · Registry & schema inference · M** — migration **028**.
Settings implemented: `partition_keys`, `full_text_search_keys`, `index_fields`,
`bloom_filter_fields`, `defined_schema_fields` (UDS), `data_retention`,
`extended_retention_days`, `flatten_level`, `max_query_range`,
`store_original_data`, `index_all_values`, `enable_distinct_fields`,
`storage_type` — plus **`cost_center`** and **`sensitivity_class`** (ours; they
feed P2.3 accounting and P5.4 redaction).
`inferSchema(samples)`: type promotion (int→float→string), nested flattening,
conflict resolution, per-field cardinality estimate.
**UI:** Settings → Streams — list, schema explorer, settings, per-stream cost.

**P4.2 · Self-serve retention · S** — compactor reads `stream_settings`; plan
limits enforced via `quota.LimitsForPlan`; an over-plan increase is **refused with
a clear message**, not silently clamped. Boundary tests per store.

**P4.3 · Schema evolution & drift · M** — new field: no reindex. Type conflict:
recorded as drift and surfaced with the offending samples, never silently coerced.

**P4.4 · Stream health · S** — ingest rate, error rate, drift events, index lag,
cost trend, with alerting hooks.

---

## P5 — Pipelines & data governance · 6 slices · 9 weeks

**Thesis.** Whoever owns transform/route/enrich owns the data. They have a no-code
editor over the real VRL crate. We match that and add the two things they cannot:
a **live debugger** and **per-record erasure**.

**Key decision — run the real VRL, not a subset.** They embed `vrl 0.31`. A Go
subset that silently mis-evaluates a pasted snippet is worse than no
compatibility. We run actual VRL as a supervised **co-process** (`pulsetrace-vrl`)
over a length-prefixed protocol on a Unix socket, bundled into the lite image so
the single-container promise holds.
_Rejected:_ a Go subset (permanent lag, false compatibility claim); cgo-linking
VRL (build fragility across eight modules).

**P5.1 · Pipeline engine · L** — migration **029**. Node graph on the ingest path
**before** the bus publish in `logbridge` and `ingestproxy`. Nodes: source →
condition → transform (VRL) → enrichment → destination (stream / remote HTTP /
S3 / Kafka). **Failure isolation:** a failing node routes its batch to a
**dead-letter stream** with the error and the offending record — one bad rule must
never silently drop a tenant's telemetry. Every save is a new version; rollback is
one click. **Budget:** a 5-node pipeline holds ingest p95 < 800 ms / p99 < 1500 ms,
wired into `ingest-load.js` as a profile.

**P5.2 · VRL sidecar · M** — supervised, health-checked, restart-with-backoff.
Ingest **fails closed** (429, not silent passthrough) when the transformer is
unavailable and the pipeline declares a required transform. Version pinned and
surfaced in the UI.

**P5.3 · Enrichment tables · M** — migration **030**. CSV/JSON upload,
tenant-scoped, cached with a size cap and LRU eviction, refreshable, versioned.
Lookups measured — an enrichment adding > 1 ms/event is flagged in the editor.

**P5.4 · Sensitive Data Redaction · M** — migration **031**. Replaces the global
4-regex middleware, including `\b(?:\d[ -]*?){13,19}\b`, which corrupts
legitimate numeric payloads today. Named detectors: email · card **with Luhn** ·
SSN · IBAN · JWT · private key · cloud credentials · custom regex. Actions:
mask / hash / tokenize / drop, per-stream and per-field. A stream marked
`sensitive` **fails closed** if its rule set does not compile; every redaction
emits an audit event. **Tests:** today's false positives (order IDs, timestamps,
version strings) become explicit negative cases. **UI:** Settings → Data
Protection — rules, live tester, per-stream coverage.

**P5.5 · Per-record erasure · L — _they cannot answer this_** — migration **032**.
`POST /api/v1/privacy/erasure` with a subject identifier and scope; executes
across Quickwit (delete-by-query + split rewrite), ClickHouse (lightweight delete
then mutation verification), Postgres, Neo4j and object storage. Asynchronous,
retried, idempotent, with a **signed certificate** enumerating what was deleted
where, exportable for a DSAR response. **Verification step:** after completion,
re-query for the subject and assert zero rows — a certificate is issued only after
that passes. Their docs concede data is immutable and only whole retention periods
can be dropped; under GDPR Art. 17 that is not a feature gap but a compliance gap.

**P5.6 · Pipeline debugger · M — _the "better, not equal" moment_** — paste a real
event, watch it traverse every node with the intermediate value at each step; see
per-node production counters (in/out/dropped/errored) once live; **replay a
dead-lettered batch** after fixing the rule. Their editor saves and hopes.

---

## P6 — Dashboards & reports · 6 slices · 9 weeks

**Thesis.** We have 17 opinionated screens and zero user-authored views — hence no
daily habit and no migration story.

**Key decision — 24 panels, and win on kind.** They ship **21** (verified). Match
the list, then add what a log tool structurally cannot: **flame graph** (reuse
Profiler), **trace waterfall** (reuse `TraceWaterfall.tsx`), **service graph**
(reuse Topology).

**P6.1 · Model, folders, versioning · M** — migration **033**. Panels/variables/
layout inside a validated `spec` JSONB; CRUD, duplicate, RBAC on write, full
version history with diff and restore. `validateDashboardSpec`: references
resolve, no cyclic variables, layout in bounds.

**P6.2 · Panel executor & visual library · L** —
`POST /api/v1/dashboards/{id}/panels/{pid}/query` executes via P3 with variables
and time range substituted **server-side** (client-side substitution would let a
user rewrite another tenant's predicate). 24 panel types; load the `dataviz` skill
before writing chart code — palette, accessibility and light/dark tokens are
specified there. `substituteVariables` is injection-tested, including a variable
whose value is SQL.

**P6.3 · Variables, filters, comparison, drilldown · M** — dependent variables
with topological resolution and cycle detection; dashboard filters; **time
comparison** (previous period as a ghost series); drilldown carrying
`{entity, time-range, filters}` into Explorer/Traces/Profiler.

**P6.4 · Annotations · S** — deploy markers (F5) and incidents as first-class
timed annotations on every panel: the visual form of "what changed."

**P6.5 · Scheduled reports · M** — migration **034**. Cron scheduler in
`correlation-service/internal/engine/` (already hosts the SLO worker); headless
render via Playwright to PDF/PNG; storage via `shared/blob`; delivery through the
**existing F3 channels** so reports reach Slack/PagerDuty/Opsgenie/webhook, not
just email — breadth they don't have. `nextRun(cron, tz, now)` tested across DST.

**P6.6 · Grafana & Datadog import · M** — `internal/dashimport/` with panel-type
maps, PromQL passthrough (works because of P3.4), Datadog widget translation, and
a **per-panel translation report**: converted / needs-attention / unsupported.
Silent partial conversion erodes trust worse than an honest failure.
Target ≥80% of panels rendering.

---

## P7 — Ingestion & migration · 7 slices · 5 weeks

**Thesis.** Catch-up, with two exceptions. Verified, they already ship `_bulk`,
`_hec`, `_kinesis_firehose`, GCP pub/sub, Loki push and `remote_write`.
**Datadog is unique to us** — defend it. **Neither has syslog** — take it.

Every slice: decode → `models.LogEntry` → meter/quota → bus publish, following the
proven `ingestproxy` pattern, with a real captured payload in `testdata/`, a fuzz
target, a `uiNone` parity registration, an Onboarding snippet, and an e2e proving
the data is queryable.

| Slice | Protocol | Why | Effort |
| --- | --- | --- | :---: |
| **P7.1** | Elasticsearch `_bulk` + `_search` subset | Largest displacement pool; Filebeat/Logstash/Fluent Bit all speak it | M |
| **P7.2** | Prometheus `remote_write` + `remote_read` | Three lines of YAML to dual-write; with P3.4 a full Prometheus drop-in | M |
| **P7.3** | **Syslog** RFC 3164/5424 TCP/UDP/TLS + journald | **Neither product has it.** Network gear, appliances, legacy fleets. Grok parsing already exists in `quickwit/kafka-source.yaml` — promote it into Go | M |
| **P7.4** | Kinesis Firehose + CloudWatch subscription | The default AWS log path; no agent install | M |
| **P7.5** | Loki push API | Catches Grafana-stack migrations mid-flight | S |
| **P7.6** | Agent recipes (Fluent Bit, Fluentd, Filebeat, Vector, Telegraf) | Config + docs on P7.1; ships via the existing snippet system | S |
| **P7.7** | OTLP/Arrow + zstd on every path | Cost per GB shipped — an efficiency win they don't advertise | S |

**Exit:** ≥13 sources, each e2e-proven and represented in the multi-protocol
performance baseline.

---

## P8 — Reliability intelligence · 6 slices · 9 weeks

**Thesis.** This is where the verification hurt. They have `evaluate`,
`reconcile`, **`backfill`**, `slo_budget_charges` — capabilities we lack. Reach
their floor, then pass them on the axis they cannot follow: **SLOs bound to the
entity graph and the deploy pipeline.**

**P8.1 · SLO v2 model · M** — migration **007**. SLI from **any P3 query**, not
just log-level counts: request-based, time-slice-based, metric-threshold.
Multi-window multi-burn-rate policies as first-class config. **Budget ledger** —
every consumption event an immutable entry, so "where did the budget go" is
answerable per deploy, per incident, per service (their `slo_budget_charges`
equivalent, plus attribution).

**P8.2 · Backfill & reconciliation · L — _closing their lead_** — migration
**008**. Backfill: define an SLO today, compute history from retained data;
resumable and rate-limited so it cannot starve live queries. Reconciliation:
periodic recompute of closed windows against source data, with divergence
recorded and surfaced. An SLO you cannot audit is a number, not an objective.

**P8.3 · Budget-driven controls · M** — burn thresholds drive the **deploy gate**
(F5): automated freeze with an explicit override that is recorded, attributed and
expiring. This is the loop they cannot close — they have no deploy intelligence to
connect a budget to.

**P8.4 · Causal anomaly detection · L** — migration **009**. Their 2,283-line
detector flags series; ours names a **suspect**. Detect per-entity (seasonal
decomposition + robust z-score + changepoint), then **walk the entity graph** to
rank candidate causes by temporal precedence and dependency distance — the same
machinery as causal RCA, run continuously. Confirm/dismiss feedback tunes
per-tenant sensitivity and becomes an eval corpus scored in CI, exactly as
`shared/causal` is today. **Measured, not asserted.**

**P8.5 · Forecasting · M** — error-budget exhaustion forecast with confidence
intervals; capacity forecast per stream from P2.3 accounting. Always displayed
with the interval, never a bare number.

**P8.6 · Reliability home · M** — one screen: every SLO, budget, burn, active
anomaly, open incident and recent deploy, ranked by business impact via the entity
graph. The daily habit for the on-call engineer.

---

## P9 — Closed-loop autonomics · 7 slices · 12 weeks — **the moat**

Everything above makes us competitive; this makes us different. One slice ships
alongside every phase from P2 onward — it does not wait for the catch-up to finish.

**P9.1 · The entity graph spine · L** — migration **010**. One canonical identity
per service/host/pod/queue/database, with alias resolution across OTel
`service.name`, k8s labels, Datadog tags and RUM app IDs. *Today the same service
has a different identity in every pillar.* Every signal carries `entity_id`; every
screen filters by it; every query joins on it. Edges: declared dependencies
(topology), observed call edges (traces), ownership (catalog), deployment targets.
**This is the architectural bet** — their streams share no entity model, so
matching it would cost them a re-architecture.

**P9.2 · Causal RCA v2 · L** — deterministic chain first (graph walk + temporal
precedence), LLM narration second: the existing, correct design, now over the
P9.1 graph and P3 joins. Evidence widened to deploys, config changes, anomaly
findings, SLO burns, profile regressions and saturation signals. **The eval
harness extends with it** — new evidence types mean new labelled fixtures and a
re-published accuracy number. We ship the score, including when it drops. Nobody
else in this market publishes one.

**P9.3 · One-click pivot invariant · M** — log → trace → span → profile → RUM
session → deploy → incident → SLO, each preserving `{entity, time-window,
filters}`, enforced by **one e2e per edge** so it cannot rot.

**P9.4 · Governed remediation · M** — the existing approval gate deepened: risk
tiers, per-action RBAC, **blast-radius preview computed from the graph** ("this
restart affects 3 downstream services with active SLOs"), mandatory dry-run for
high-risk, full attribution.

**P9.5 · Verification & auto-revert · L — _nobody ships this_** — migration
**011**. After execution, watch the SLI that triggered the incident for a
configured window: improved → record success; not improved or degraded →
**automatically revert** and re-open with the failed hypothesis attached. The
whole loop lands in the incident timeline: proposed → approved → executed →
verified / reverted.
**Failure modes matter most here:** revert must be idempotent; must not fight a
human operator (a manual change during the window aborts auto-revert and
escalates); and must fail safe to "leave it alone and page."

**P9.6 · LLM observability with cost attribution · L** — migration **012**. OTel
GenAI semconv ingest. They ship `llm_evaluations`, so eval scores are **not** the
differentiator — **per-feature, per-tenant, per-customer cost attribution** is,
plus guardrail-violation alerting, prompt-version regression diffs, and causal RCA
over LLM incidents through the same engine as everything else.

**P9.7 · AI dashboard authoring · M** — "show me checkout health" → a complete
dashboard grounded in the P4 schema and the P9.1 graph, every panel's SQL visible
and editable, reusing the F15 hallucination guardrail so it cannot invent a field.
Their AI writes a query; ours writes the workspace.

---

## P10 — Enterprise & scale · 6 slices · 10 weeks

**P10.1 · Federated search · L** — migration **037**. Leader fans out over gRPC,
merges, and reports per-cluster latency and partial failures **honestly**: a
timed-out region renders as degraded, never silently omitted. Region-pinned
storage per tenant for residency.

**P10.2 · Workload management · M** — migration **035**. Per-role/tenant
concurrency, priority queue, byte/row/time budgets, **kill-query**, slow-query
log. Theirs is enterprise-gated; shipping it in OSS is a positioning win worth
taking.

**P10.3 · LDAP / Active Directory · S** — behind the provider interface OIDC and
SAML already share; group→role mapping mirrors SCIM.

**P10.4 · BYOK · M** — migration **036**. Tenant KEK from KMS/Vault, envelope
encryption, rotation, re-wrap job; fail-closed, matching `CHANNEL_ENCRYPTION_KEY`.
⚠ Re-scope first: a cipher-key registry is in *their* OSS, so this is not the
clean enterprise gap v1 assumed.

**P10.5 · Compliance · L — starts week one of P1.** Calendar-bound, not
effort-bound: SOC 2 Type II needs an observation window, so the earliest credible
date is ~9 months from kickoff. The evidence largely exists — hash-chained audit
(F20), tenant-isolation ratchet (F0.3), DR posture, security scanning at zero
call-reachable CVEs, the parity gate. Assemble the control mapping, pick an
auditor, open the window. ISO 27001 and a HIPAA BAA follow the same evidence.

**P10.6 · Multi-region active/standby · L** — cross-region replication, hot
failover, a rehearsed RTO drill.

---

## 3. Performance budgets

CI-enforced; a regression fails the build.

| Path | Budget |
| --- | --- |
| Ingest, any protocol | p95 < 800 ms · p99 < 1500 ms · errors < 2%, **with a 5-node pipeline active** |
| In-process bus | ≥50k msg/s publish · p99 < 5 ms at 20k msg/s |
| Log search (needle, 7 d) | p95 < 2 s cold · < 400 ms cached |
| SQL aggregation (1 B rows) | p95 < 10 s · first byte < 500 ms |
| PromQL range (24 h @ 1 m) | p95 < 1.5 s |
| Dashboard load (12 panels) | p95 < 3 s, panels progressive |
| Lite cold start | < 60 s to first queryable datapoint |
| Storage | bytes/GB ingested ≤ OpenObserve on the P0 corpus |
| Compactor | backlog drains faster than ingest at 2× peak |

---

## 4. Risk register

| Risk | Sev | Mitigation |
| --- | :---: | --- |
| P3.1 multi-tenant SQL leaks across tenants | **critical** | Physical isolation via tenant-bound scanners — the attack is inexpressible, not merely blocked; plus AST rewriting, store guard, escape suite, mandatory security review, and a ship-single-tenant fallback |
| P1.3 SQLite lock forks the audit hash chain | **critical** | `BEGIN IMMEDIATE` + `chain_lock`; a concurrency test that provably forks a naive implementation must pass |
| P2.2 retention off-by-one deletes live data | **critical** | Boundary-tested `partitionsToDrop`; a full dry-run retention cycle before enforcement |
| P5.4 redaction rewrite regresses protection | high | New detectors pass the old suite plus false-positive cases; both paths behind a flag for one release |
| P1 ports destabilise the cluster path | high | Cluster stays default; conformance suite runs both; one port per commit; the existing suite is the acceptance test |
| Estimates optimistic | high | Re-forecast after P1 — the first phase touching every service |
| P5.2 VRL sidecar breaks the one-container promise | medium | Bundled in the lite image; supervised with backoff; ingest fails closed, never silently untransformed |
| DuckDB (cgo) complicates the build | medium | Isolated behind the `analytics` port; pinned version; cross-compilation verified in CI in the first week, not the last |
| P8 merely reaches SLO parity | medium | The differentiator is P8.3 (budget→deploy gate) and P8.4 (causal anomalies), not the engine. **Do not declare P8 done at parity** |
| Moat deferred behind catch-up | **strategic** | One P9 slice alongside every phase from P2, enforced at planning time |

---

## 5. Execution order — enterprise / regulated motion

**Decision (2026-08-13): the buyer is enterprise / regulated.** Compliance, audit
and governance decide these deals; the buyer runs Helm and does not care that the
stack is 23 containers. That inverts the naive phase order — §2's numbering is the
*dependency* map, this section is the *execution* map, and this section wins.

### 5.1 The principle: threshold parity, not full parity

There is a difference between **winning** a dimension and **not being
disqualified** on it. Most of P1–P7 is the latter. Competing head-to-head on
cost-and-simplicity — their strongest axis, better resourced — while our
differentiation ages is the losing line. We cross the threshold on their axis and
spend the surplus on ours.

| Parity item | Verdict for this motion |
| --- | --- |
| Dashboards (P6) | **Disqualifying.** No custom views = not a real observability product |
| Query language (P3) | **Disqualifying.** A technical evaluator who cannot ask their own question walks |
| Per-stream retention (P4) | **Disqualifying.** "Keep audit 400 days" is a compliance requirement, not a preference |
| Data redaction (P5.4) | **Disqualifying.** PII handling is a standard procurement question, and ours is 4 regexes — one of which corrupts payloads |
| Ingestion `_bulk` / remote-write (P7.1–7.2) | Needed for migration off an incumbent |
| Single-container lite (P1) | **Deferred.** Enterprises deploy Helm; this is a self-serve concern |
| Storage economics (P2) | **Deferred** pending P0 — Quickwit is tantivy, so the log tier may already be at parity |
| Pipelines (P5.1–5.3) | **Deferred.** Vector and Fluent Bit already do this upstream |
| Federation (P10.1) | Deferred, but **first to be pulled forward** — EU/US data residency shows up in enterprise deals |

### 5.2 Two concurrent tracks

**Track 1 — cross the threshold (~26 eng-weeks)**
1. **P0.1–P0.2** (2 wks) — the harness. Cheap, and it tells us whether P2 is even
   a real gap before we budget six weeks for it.
2. **P3.1** — the query core. Long pole, security-critical, needs review time.
3. **P3.2 · P3.3 · P3.6** — SQL endpoint, cross-signal joins, workbench.
4. **P6** — dashboards, the missing daily habit.
5. **P4** — streams, schema, self-serve retention.
6. **P7.1 · P7.2** — `_bulk` and Prometheus remote-write, the migration wedges.

**Track 2 — deepen the moat, from week one (~20 eng-weeks)**
1. **P10.5** — open the SOC 2 observation window **immediately**. Calendar-bound:
   starting it in month nine costs a year, and the evidence already exists.
2. **P9.1** — the entity graph. Everything in P8 and P9 keys to it; retrofitting
   later means touching every screen twice.
3. **P5.5** — per-record erasure. **The one architecturally durable advantage we
   have**, and it is a compliance claim, not a feature claim.
4. **P5.4** — real SDR, replacing the regex middleware.
5. **P8.3** — budget-driven deploy gate: burn attributed to a change.
6. **P9.4 · P9.5** — governed remediation with verification and auto-revert.

**Then, as procurement demands:** P10.3 (LDAP), P10.2 (workload management),
P10.4 (BYOK), P10.1 (federation/residency).

### 5.3 Standing instruction

**Stop leading with "observability platform."** On that framing we lose on cost
and breadth to a better-resourced team. Lead with **change intelligence and
governed automation**, where the comparison is "we have it, they do not."

### 5.4 Durability of our advantages — plan accordingly

Only one of our verified wins is architecturally safe. Treat the rest as a lead to
extend, not a position to hold.

| Advantage | Their time-to-match | Implication |
| --- | --- | --- |
| **Per-record erasure** | **Blocked** — immutable Parquet | Press it hard; make it a headline |
| Deploy gates / DORA / change-failure | 6–12 mo (needs an entity model they lack) | Deepen while the lead exists |
| Continuous profiling | 6–12 mo (a whole pillar) | Wire it into the incident loop (P9.3) so it is not a standalone feature |
| Governed remediation | ~3 mo — they have the workflow engine already | Verification + auto-revert (P9.5) is the part that stays hard |
| Tamper-evident audit | **~2 weeks** — theirs is 105 lines because nobody asked | Not a moat. A checkbox we happen to have |
| Measured RCA eval | A discipline, not a feature | The moat is the *habit* of publishing the score |

**The load-bearing bet is P9.1.** A feature is copied in a quarter; an
architecture is not. If the entity graph does not get built, this plan degrades
into being a slower OpenObserve.

---

### One paragraph

Reach parity on the substrate (P1–P7) without pretending it is a differentiator;
close the SLO gap we actually have (P8); and spend the difference on what their
architecture cannot copy quickly — one entity graph, causal explanation, governed
action, and **verification that the action worked** (P9). We do not win by being a
faster OpenObserve. We win by being the system that closes the loop, with the
audit trail to prove it.
