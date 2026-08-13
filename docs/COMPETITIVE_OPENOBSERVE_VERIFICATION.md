# OpenObserve — Source-Verified Findings

_Verification pass run 2026-08-13 against `openobserve/openobserve` @ `main`
(shallow clone): **1,419 Rust files · 576,488 LOC · 6,048 files**._

The first version of [COMPETITIVE_OPENOBSERVE.md](COMPETITIVE_OPENOBSERVE.md) was
written from **five web pages** — the GitHub README and four openobserve.ai docs
pages. No source was read. This document records what reading the source actually
showed, with file citations, and is the authority where the two disagree.

## The headline: the first analysis overstated PulseTrace's position

Three claimed PulseTrace wins do not survive contact with the source. One is
flatly wrong, two are downgraded. The corrected score is **OpenObserve leads ~14,
PulseTrace leads 4 confirmed + 1 contested** — not the 15–6 originally published.

---

## 0. A caveat that governs everything below

**`src/enterprise/` in the OSS repo is 64 lines across four stub crates:**

| Stub | Lines | What it really is |
| --- | ---: | --- |
| `src/enterprise/o2_enterprise/lib.rs` | 16 | Enterprise feature gate |
| `src/enterprise/o2_dex/lib.rs` | 16 | SSO federation (OIDC/SAML/LDAP) |
| `src/enterprise/o2_openfga/lib.rs` | 16 | Relationship-based authz |
| `src/enterprise/o2_ratelimit/lib.rs` | 16 | Query/workload rate limiting |

Their commercial build is closed-source. So for **D11 (SDR), D13 (auth breadth),
and parts of D12 (federation)** this pass can only say "not verifiable from OSS" —
the marketing claims may be entirely true in the paid product. Every verdict below
is scoped to *the open-source repository*, and says so where it matters.

---

## 1. Verdict table

Legend: ✅ my original claim confirmed · ❌ wrong · ⚠️ downgraded/qualified

| # | Dimension | Original claim | Verified verdict | Evidence |
| --- | --- | --- | --- | --- |
| D1 | Deployability | OO wins | ✅ | `src/config/src/meta/cluster.rs:203-222` — `Role::{Ingester,Querier,Compactor,Router,AlertManager,FlattenCompactor,All}`; `src/infra/src/db/mod.rs:40` `SQLITE_STORE` alongside Postgres |
| D2 | Storage economics | OO wins | ✅ | `src/ingester/src/{memtable,immutable,wal,writer}.rs`; `src/compaction/src/{merge,retention,bloom,incremental,dump,flatten}.rs`; `src/tantivy_utils/src/{index_builder,puffin}` — inverted index in Puffin next to Parquet |
| D3 | Query power | OO wins | ✅ **understated** | DataFusion (`Cargo.toml:123`) + `src/search/src/{sql,datafusion,tantivy,cache,bloom_pruner.rs}`; a full PromQL engine at `src/promql/src/{engine,exec,aggregations,binaries,functions}` |
| D4 | Dashboards | OO wins, "19+ charts" | ✅ **21 verified** | area, area-stacked, bar, custom_chart, donut, gauge, geomap, h-bar, h-stacked, heatmap, html, line, maps, markdown, metric, pie, sankey, scatter, stacked, table, treemap. Plus `src/core/src/dashboards/timed_annotations.rs` |
| D5 | Streams/schema | OO wins | ✅ **understated** | `src/config/src/meta/stream.rs:941-971` — `StreamSettings` carries 15 fields incl. `defined_schema_fields` (UDS), `extended_retention_days`, `index_all_values`, `store_original_data`, `approx_partition`, `max_query_range`, `bloom_filter_fields` |
| D6 | Pipelines | OO wins | ✅ **harder than assumed** | `Cargo.toml:612` — the **real `vrl = 0.31` crate**, not a subset; `src/enrichment_data/src/{enrichment,enrichment_table}`; `src/core/src/pipeline/batch_execution.rs` |
| D7 | Ingestion breadth | OO wins; "DD + Splunk HEC are ours alone" | ⚠️ **half wrong** | They ship `_hec` (**Splunk HEC**), `_bulk`, `_kinesis_firehose`, GCP pubsub, `loki/api/v1/push`, OTLP, Prometheus remote-write (`src/core/src/metrics/prom.rs`), `sessionreplay` — see `src/api/ingest/src/request/`. **Datadog is genuinely ours alone.** Neither has a syslog listener. |
| D8 | Reports | OO wins | ✅ | `src/report_server/src/{report,router,server,models}.rs` |
| D9 | Session replay | OO wins | ✅ | `POST /v1/{org_id}/replay` → `pub async fn sessionreplay` |
| D10 | LLM observability | OO wins | ✅ | `src/core/src/llm_evaluations/{eval_jobs,evaluator_trace_exporter.rs}`; plus an MCP server at `src/mcp/src/{tools,protocol,handler}.rs` and `web/src/components/ai_toolsets/` |
| D11 | Data controls / SDR | OO wins | ⚠️ **not verifiable** | `src/cipher/src/{registry,enterprise}.rs` exists; the SDR itself is behind the enterprise stub |
| D12 | Federation | OO wins | ✅ | `src/super_cluster_queue/src/` — 10+ modules (alerts, dashboards, cipher_keys, destinations, domain_management, …) |
| D13 | Auth breadth | OO wins on LDAP | ⚠️ **not verifiable** | OSS has JWT + user meta only (`src/api/common/src/auth/jwt.rs`); OIDC/SAML/LDAP live in the 16-line `o2_dex` stub |
| D14 | Compliance certs | OO wins | — | Not a code question; unchanged |
| D15 | Proven scale | OO claims | — | Still their published claim; unchanged |
| D16 | Incident lifecycle & RCA | **PT wins** | ⚠️ **downgraded to contested** | They have `src/core/src/incidents.rs` (248 L), `src/infra/src/table/{incident_events,alert_incidents}.rs`, a workflow engine (`src/core/src/workflows/mod.rs` 810 L + `runtime.rs`), `action_scripts` tables, and a migration creating **`sys_rca_agent_service_accounts`**. No approval-gate / risk-tier / dry-run semantics found in OSS, and no counterpart to our causal **eval harness** — but "nothing comparable" was wrong |
| D17 | SLO engineering | **PT wins** | ❌ **WRONG — reverse it** | Full subsystem: `src/core/src/slo/{job,query,ingest,service,reconcile,evaluate,writer,backfill}.rs`; tables `slo`, `slos`, `slo_budget`, `slo_budget_charges`, `slo_backfill_jobs`, `slo_status`; 8 Vue components incl. `SloBurndownChart`, `SloPreviewChartPromql`, `SloTimeSlicePreview`, and `burn_rate` / `error_budget` alert conditions. **Backfill, reconciliation and budget charges are things we do not have.** |
| D18 | APM pillar depth | **PT wins** | ⚠️ **partly wrong** | **Synthetics is theirs too** (`src/core/src/synthetics.rs`, `src/infra/src/table/synthetics_checks.rs`, two UI dirs). **Anomaly detection is theirs too** (`src/core/src/anomaly_detection.rs`, 2,283 L). **Continuous profiling remains ours** — their only `profiling.rs` is `src/api/management/` server self-profiling, and their flame graph (`web/src/components/traces/FlameGraphView.vue`) renders trace spans, not profiles. No counterpart found for deploy gates / DORA / service catalog |
| D19 | Deletability | **PT wins** | ✅ **confirmed** | `src/compaction/src/deleted.rs` and `src/infra/src/file_list/pending_delete.rs` are **internal compacted-file GC**, not user-facing record deletion. No GDPR/erasure path in source; "gdpr" appears only in `README.md` |
| D20 | Audit integrity | **PT wins** | ✅ **confirmed** | `src/audit/src/lib.rs` is **105 lines** — a publisher that ships audit messages to a stream. No hash chain, no prev-hash, no verify endpoint |
| D21 | Engineering discipline | PT wins | — | Internal to us; unchanged |

---

## 2. What this changes

### 2.1 Claims to delete from the pitch

- **"They have no SLO product."** They have a deeper one than ours.
- **"They have no synthetics."** They do.
- **"Anomaly detection is our differentiator."** 2,283 lines say otherwise.
- **"Datadog + Splunk HEC ingest is ours alone."** Only Datadog is.
- **"Nothing comparable to self-healing."** They have a workflow engine, action
  scripts, an MCP tool server, and an RCA agent service account.

### 2.2 Claims that survive, and are now citable

- **Continuous profiling** — no product pillar on their side.
- **Deletability** — architecturally hard for them; their own docs concede it.
- **Tamper-evident audit** — 105 lines vs our hash-chained, verifiable trail.
- **Deploy gates / DORA / change-failure linking / service catalog** — no counterpart found.

### 2.3 Plan changes required

| Plan item | Problem found | Correction |
| --- | --- | --- |
| **P5.2** — "20 panel types, beating their 19" | They have **21** | Target ≥22, and compete on *kind* (flame-graph and trace-waterfall panels) not count |
| **P6.3** — "a Go-native VRL subset" | They embed the **real `vrl` crate** | A Go subset will always lag. Either run VRL in a sidecar process, or drop the compatibility claim and ship our own documented language. Do not promise "VRL-compatible" on a subset |
| **P7.1/P7.3** — Splunk HEC as a differentiator | They have `_hec` | Keep `_bulk` and remote-write as the gaps to close; **Datadog** is the only migration path unique to us |
| **P8.4** — BYOK framed as an enterprise gap | `src/cipher/` is partly in OSS | Re-scope to what their OSS actually lacks; verify against the paid product before claiming |
| **PM.4** — "LLM observability done properly" | They ship `llm_evaluations` | Sharpen the delta to cost-attribution + causal RCA over LLM incidents; drop "eval scores" as a differentiator |
| **PM.5** — closed-loop remediation | They have workflows + action scripts | The delta is the **approval gate + risk-tier authz + SLO verification + auto-revert**, not automation itself |
| **§3 "protect and widen"** | Listed SLOs and synthetics as moats | Both removed; see §2.2 for what actually remains |

### 2.4 A new gap this pass revealed

Their `StreamSettings` (D5) is materially richer than the docs implied —
`index_all_values`, `store_original_data`, `approx_partition`, `max_query_range`,
`bloom_filter_fields`, `flatten_level`. **P6.1 should target that field list**,
not the four settings the docs mentioned.

---

## 3. Method, and its limits

What was done: shallow clone; structural map of 38 crates by LOC; targeted reads
of the module owning each dimension; grep-verified feature presence with file
citations.

What was **not** done, and should not be claimed:

- **Nobody read 576k LOC**, including this pass. Presence of a module proves the
  feature exists; it does not measure quality, performance, or completeness.
- **Depth comparisons are inferred from structure** (file count, LOC, table
  schemas, UI components), not from running either product. The D17 conclusion
  that their SLO engine is "arguably deeper" rests on their having backfill and
  reconciliation modules we lack — a structural argument, not a benchmark.
- **The enterprise build was not seen at all** (§0).
- **No behaviour was executed.** Wave **P0** of the implementation plan — the
  side-by-side benchmark harness — is still the only thing that will turn any
  performance or cost claim into a fact. This pass corrects the *feature* map;
  it does nothing for D2 or D15.
