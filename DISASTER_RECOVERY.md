# Disaster Recovery & Backup Posture

_PulseTrace · F21. This is the documented DR posture the deployment bar (ROAD_TO_100 · F21) requires: what we protect, how fast we recover, how each store is backed up and restored, and where the current design stops (single-region) and the future work begins (multi-region)._

Scope: the self-managed **enterprise / on-prem** deployment (Helm chart in [`helm/`](helm/), raw manifests in [`k8s/`](k8s/)) and the **SaaS** control plane. Where the two differ, both are called out.

---

## 1. Objectives (RPO / RTO)

RPO = maximum acceptable data loss. RTO = maximum acceptable time to restore service.

| Data tier | Contents | RPO target | RTO target |
| --- | --- | --- | --- |
| **T1 — system of record** | Postgres: users, RBAC/ABAC, tenants, incidents, playbooks, **audit log**, sessions, SLOs, alert rules, ingestion keys, usage | ≤ 5 min | ≤ 1 hr |
| **T2 — telemetry** | ClickHouse: traces, logs, metrics, RUM, synthetic results | ≤ 1 hr | ≤ 4 hr |
| **T3 — derived / rebuildable** | Neo4j topology, Quickwit log index, Pyroscope profiles | best-effort (rebuilds from source) | ≤ 8 hr |
| **T4 — transient** | Kafka, RabbitMQ, Redis | none (replayable / re-derived) | ≤ 1 hr |

The audit log (T1) is **hash-chained and tamper-evident** (F20); backups preserve the chain, and a restore is verifiable end-to-end via `GET /api/v1/admin/audit-log/verify`, which is itself a DR-integrity check.

---

## 2. Data inventory & criticality

| Store | Volume | Role | If lost |
| --- | --- | --- | --- |
| **Postgres** (`postgres_data`) | small | Authoritative business state | **Unrecoverable without backup** — the one store that must never lose data |
| **ClickHouse** | large | Observability telemetry | Historical visibility gap; new telemetry keeps flowing |
| **Neo4j** (`neo4j_data`) | small | Service topology + causal paths | Rebuilds as agents re-push state; causal history lost |
| **Quickwit** (`quickwit_data` + object store) | medium | Full-text log index | Rebuildable by re-indexing from the object-store source |
| **Kafka** (+ ZooKeeper) | medium | In-flight ingestion/alert pipeline | Buffered events only; producers replay |
| **RabbitMQ** (`rabbitmq_data`) | small | Incident/notification queue | In-flight notifications only |
| **Redis** | small | Metering hot counters | ≤ one flush interval (30 s) of usage counts; reconciled from Postgres `usage_daily` |
| **Object storage** (S3/GCS/Azure Blob; MinIO/Azurite in dev) | large | Quickwit segments, exports | Backs Quickwit; use the provider's own durability + versioning |

---

## 3. Backup strategy

**Principle:** managed backends in SaaS, native tooling on-prem. Backups are encrypted at rest, stored in a **different failure domain** than the source (separate region/account), and retention is tiered (daily 30d, weekly 90d, monthly 1y for T1).

| Store | SaaS | Enterprise / on-prem |
| --- | --- | --- |
| **Postgres (T1)** | Managed PITR (e.g. RDS/Cloud SQL automated backups + WAL, 5-min RPO) | `pg_basebackup` + continuous WAL archiving (PITR), or nightly `pg_dump` for the floor; ship WAL to object storage |
| **ClickHouse (T2)** | Managed backups / `BACKUP ... TO S3` | [`clickhouse-backup`](https://github.com/Altinity/clickhouse-backup) to object storage, incremental daily |
| **Neo4j (T3)** | Managed / `neo4j-admin database backup` | `neo4j-admin database dump` nightly to object storage (online backup on enterprise) |
| **Quickwit (T3)** | Index metadata (in Postgres/managed) + segments already in object storage | Back up the Quickwit metastore; segments live in the durable object store already |
| **Object storage** | Provider durability (11 9s) + versioning + cross-region replication | Same — enable versioning + replication; this is a dependency, not something we snapshot |
| **Kafka / RabbitMQ / Redis (T4)** | Not backed up — replayable / re-derived | Same. Redis is a cache flushed to Postgres; Kafka retention covers replay window |

**Secrets & config are not in these backups.** They are owned by the secret backend (Vault / cloud secret manager) via the External Secrets Operator ([`k8s/externalsecret.yaml.example`](k8s/externalsecret.yaml.example)); that backend has its own DR. Restoring PulseTrace re-syncs secrets from it — we never restore a plaintext Secret. The Helm chart + `k8s/` manifests are the reproducible infra definition (in git); a cluster is rebuilt from them, not from a cluster snapshot.

---

## 4. Restore runbooks (high level)

Restore order respects dependencies: **secrets → Postgres → ClickHouse/Neo4j → app → telemetry replay**.

1. **Provision infra** — new cluster from the Helm chart; install the External Secrets Operator; the `SecretStore` re-materializes `pulsetrace-secrets` from the secret backend. Do **not** apply a hand-managed `secret.yaml`.
2. **Restore Postgres (T1) first** — PITR to the target timestamp (or latest base + WAL). This is the gating step; nothing else matters if T1 is wrong. Then run the app once so schema migrations reconcile (idempotent), and **verify the audit chain** (`/api/v1/admin/audit-log/verify`) to prove integrity.
3. **Restore ClickHouse (T2)** — `clickhouse-backup restore` (or managed restore) of the latest telemetry backup.
4. **Restore/rebuild T3** — `neo4j-admin database load`; point Quickwit at its object-store segments and restore its metastore. If a T3 backup is stale, let it rebuild from live agent pushes + re-indexing rather than blocking RTO.
5. **Bring up the app** — `helm upgrade --install` pinned to a known-good image tag. Health-gate on `/healthz`.
6. **Drain the transient tier** — Kafka/RabbitMQ producers replay from their retention window; Redis counters re-warm and reconcile against `usage_daily`.

Each step has an explicit success check; a step that can't be verified is treated as failed and retried before moving on.

---

## 5. Failure scenarios & response

| Scenario | Response | Design today |
| --- | --- | --- |
| **Pod / node loss** | Kubernetes reschedules; HPAs + PDBs ([`k8s/poddisruptionbudgets.yaml`](k8s/poddisruptionbudgets.yaml), Helm `hpa.yaml`/`pdb.yaml`) keep capacity and cap voluntary disruption | ✅ handled in-cluster, no DR event |
| **Availability-zone loss** | Multi-AZ node pools + managed multi-AZ datastores ride it out | ✅ with a multi-AZ cluster/datastore setup (deployer-provided) |
| **Region loss** | Restore from cross-region backups into a standby region (§4). Meets T1 RTO ≤ 1 hr | ⚠️ restore-based (not hot standby) — see §6 |
| **Data corruption / bad migration** | Postgres PITR to just before the event; migrations are forward-only + idempotent | ✅ PITR window covers it |
| **Accidental / malicious deletion** | PITR + object-store versioning; the tamper-evident audit log shows who/what | ✅ |
| **Tenant-level data loss** | Not DR — the F19 purge path (`/admin/tenant/purge-data`) is deliberate and certificate-evidenced | n/a |

---

## 6. Failover posture — current vs. future

- **Today (single-region HA):** within a region we survive pod/node/AZ failure via Kubernetes + multi-AZ managed datastores. A full **region loss** is handled by **restore-from-backup into a standby region** — meeting the RTO/RPO in §1, but with a manual, backup-driven cutover rather than an automatic hot failover.
- **Deferred (multi-region active/standby):** cross-region streaming replication for Postgres/ClickHouse, a warm standby control plane, DNS/global-LB health-based cutover, and regular failover drills. Per ROAD_TO_100 · F21 this is intentionally **not built speculatively** (currently ~15%); it's the first thing to revisit when a customer's contractual RTO drops below the restore-based window above.

---

## 7. Testing & drills

A DR plan that isn't exercised is a hypothesis. Cadence:

- **Quarterly:** full T1 restore drill into a scratch environment; confirm RTO and **audit-chain verification** pass. This is the drill that matters most.
- **Semi-annual:** full-stack restore (T1+T2+T3) end-to-end; measure against the RTO table.
- **Continuous:** backup jobs emit success/failure metrics into PulseTrace itself; a missed backup raises an alert like any other SLO breach.
- **On every release:** the Helm chart renders and the images build/push in CI (`helm` + `docker-build` jobs), so the "provision infra" restore step is always known-good.

---

## 8. Ownership

| Area | Owner |
| --- | --- |
| Backup jobs & retention | Platform / SRE |
| Restore runbook & quarterly drill | Platform / SRE (on-call runs the drill) |
| Secret backend DR | Security |
| This document | kept current with each new stateful dependency or secret (see `k8s/secret.yaml.example` ↔ `externalsecret.yaml.example`) |
