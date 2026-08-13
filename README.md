# PulseTrace — Distributed Observability & Incident Monitoring Platform

A production-grade, event-driven observability platform for microservices, built with Go and Next.js.

> Think mini-Datadog / mini-Grafana — built from scratch, with analytical-grade telemetry storage, multi-cloud cold tiering, AI-driven root cause analysis, and full-stack APM features (Traces, Profiling, RUM, Synthetics).

---

## Architecture

```
 INGEST   OTel SDKs (OTLP/gRPC :4317 · OTLP/HTTP /v1/*) · Datadog agents (/v0.x/traces,
          /api/v2/logs) · Splunk forwarders (/services/collector) · RUM browser SDK · Vector
                                        │
          ┌─────────────────────────────▼──────────────────────────────────────┐
          │                      API Gateway  :8080                             │
          │  reverse proxy · W3C trace context · OTLP receiver · ingest proxy   │
          │  auth (OIDC/SAML/SCIM/MFA) · RBAC+ABAC · quota · metering · PII     │
          └──┬──────────┬───────────┬────────────┬──────────────────┬──────────┘
             │          │           │            │                  │
      ┌──────▼─────┐ ┌──▼──────┐ ┌──▼─────────┐ ┌▼───────────┐      │ OTLP spans
      │    Log     │ │  Alert  │ │Correlation │ │  Topology  │      │ (traces,
      │   :8081    │ │  :8082  │ │   :8083    │ │   :8084    │      │  metrics)
      └──────┬─────┘ └──┬──────┘ └──┬─────────┘ └─────┬──────┘      │
             │          ▲           │  SLO worker     │             ▼
      Kafka "logs"──────┘    Kafka "alerts"           │      OTel Collector
             │          (ERROR/FATAL)  │              │             │
             ▼                         ▼              ▼             ▼
   ┌──────────────────┐          RabbitMQ          Neo4j       ClickHouse
   │    Quickwit      │              │        (service graph   otel_traces
   │ pulsetrace-logs  │              ▼         + catalog)      otel_metrics_*
   │ dynamic schema   │       Notification :8086                rum_events
   │ splits on disk   │   Slack · PagerDuty · Opsgenie       synthetic_results
   └──────────────────┘   email · signed webhook                   │
     ▲        ▲                                          tiered storage policy
     │        │                                                    │
  log search  SLI / error-budget queries          ┌────────────────┼──────────────┐
  (Explorer)  (correlation-service)               ▼                ▼              ▼
                                            AWS S3 / MinIO   Azure Blob /    GCP GCS
                                            (s3_cold)        Azurite         (gcp_cold)

 PostgreSQL   alerts · incidents · SLO definitions + SLI snapshots · RBAC/ABAC · users
              sessions · ingestion keys · billing · deploy gates · hash-chained audit

 SIDECARS     action-service :8085 (executes approved playbooks, HMAC-signed) ·
              Redis (key/session caches) · Pyroscope :4040 (continuous Go profiling) ·
              Jaeger :16686 + Prometheus :9090 + Grafana :3000 (self-monitoring)
```

> **Note on log storage:** logs are indexed by **Quickwit**, not ClickHouse. Every
> log path — native, OTLP (via the gateway's log bridge), Datadog and Splunk —
> converges on the Kafka `logs` topic, which Quickwit consumes into the
> `pulsetrace-logs` index. ClickHouse serves traces, metrics, RUM and synthetics.

---

## What's Built

| Component                  | Responsibility                                                                          |
|----------------------------|-----------------------------------------------------------------------------------------|
| `gateway-service`          | Reverse proxy, W3C trace context, OTLP receiver, Datadog/Splunk ingest proxy, auth (OIDC/SAML/SCIM/MFA), RBAC+ABAC, quota + metering, billing, hash-chained audit, Synthetics engine, RUM endpoint, traces/metrics/errors/profiler/deploy APIs |
| `log-service`              | Ingest logs → publish to Kafka; serves the log **search** API over Quickwit (service/level/trace filters, phrase + regex, relative or absolute time, surrounding-context) |
| `alert-service`            | Consume `logs` topic, create alerts for ERROR/FATAL, publish to `alerts` topic; alert silences & maintenance windows |
| `correlation-service`      | Consume `alerts`, group into incidents, causal RCA, SLO burn-rate engine, anomaly detection, AI postmortems, remediation approval gate, AI-SRE chat |
| `topology-service`         | Auto-discovers service dependencies and maintains the ownership Catalog in Neo4j        |
| `action-service`           | Executes approved remediation playbooks; verifies HMAC-signed requests and enforces its own remediation mode |
| `notification-service`     | Consume RabbitMQ, dispatch to Slack / email / PagerDuty / Opsgenie / webhook / log; per-tenant DB-backed channels with encrypted secrets |
| `frontend`                 | Next.js dashboard — 17 screens: AI SRE, Incidents, Alerts, SLOs, Deploy Gates, Onboarding, Log Explorer, Traces, Services, Metrics, Errors, Profiler, RUM, Synthetics, Topology, Catalog, Settings |
| `shared`                   | Models, DB pools (Postgres + ClickHouse), Kafka, RabbitMQ, causal engine + eval harness, remediation policy, metering, OTel middleware |
| **Quickwit**               | **Log index (`pulsetrace-logs`) — dynamic schema, Kafka-sourced, splits on disk/object store. The log store.** |
| ClickHouse                 | Column-oriented analytical store for traces, metrics, RUM and synthetic results (hot SSD tier) |
| Neo4j                      | Service dependency graph + catalog (topology, blast radius, causal paths)               |
| PostgreSQL                 | Alerts, incidents, SLO definitions + SLI snapshots, RBAC/ABAC, users, sessions, ingestion keys, billing, deploy gates, audit chain |
| Kafka                      | Event bus: `logs` and `alerts` topics                                                   |
| RabbitMQ                   | Notification pipeline with dead-letter queue                                            |
| Redis                      | Ingestion-key and session caches                                                        |
| Vector                     | Host/container log shipper into the native log path                                     |
| MinIO                      | Local S3-compatible emulator for the ClickHouse cold tier (dev/test)                    |
| Azurite                    | Local Azure Blob Storage emulator for the ClickHouse cold tier (dev/test)               |
| OTel Collector             | Receives OTLP spans/metrics, forwards to Jaeger and ClickHouse                          |
| Jaeger                     | Distributed trace visualization (self-monitoring)                                       |
| Pyroscope                  | Continuous CPU and memory profiling for the Go services                                 |
| Prometheus                 | Metrics scraping from OTel Collector                                                    |
| Grafana                    | Pre-provisioned dashboards for traces and metrics                                       |

---

## Quick Start

```bash
# 1. Clone
git clone <repo-url> pulsetrace && cd pulsetrace

# 2. Build and start the full stack
#    MinIO (S3) and Azurite (Azure Blob) emulators start automatically
docker compose up --build

# 3. Ingest an INFO log (no alert)
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service": "auth-service", "level": "INFO", "message": "user login successful"}'

# 4. Ingest an ERROR log (triggers alert → incident → notification)
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service": "payment-service", "level": "ERROR", "message": "DB connection pool exhausted"}'

# 5. Search logs (served from Quickwit)
curl "http://localhost:8080/api/v1/logs?service=payment-service&level=ERROR&since=15m"

# 6. Query alerts
curl "http://localhost:8080/api/v1/alerts"

# 7. Query incidents (grouped + root cause)
curl "http://localhost:8080/api/v1/incidents"

# 8. Get incident timeline
curl "http://localhost:8080/api/v1/incidents/<id>/timeline"

# 9. Create an SLO definition
curl -X POST http://localhost:8080/api/v1/slo/definitions \
  -H "Content-Type: application/json" \
  -d '{"service_name": "payment-service", "slo_target": 99.9, "sli_type": "availability", "window_days": 30}'

# 10. View SLO dashboard (burn rate, error budget, trend)
curl "http://localhost:8080/api/v1/slo/dashboard"

# 11. Move a traces partition to cold storage manually (ClickHouse CLI)
docker exec -it pulsetrace-clickhouse-1 clickhouse-client \
  --query "ALTER TABLE otel_traces MOVE PARTITION '$(date +%Y%m)' TO DISK 's3_cold'"

# 12. Start the dashboard (not part of docker compose)
cd frontend && npm install && npm run dev   # http://localhost:3000
```

> ⚠️ The dev dashboard and Grafana both default to port **3000**. Run the
> frontend on another port (`npm run dev -- -p 3001`) if you need Grafana up at
> the same time.

---

## Observability UIs

| UI                    | URL                        | Credentials                    |
|-----------------------|----------------------------|--------------------------------|
| **PulseTrace**        | http://localhost:3000      | created on first run           |
| Jaeger                | http://localhost:16686     | —                              |
| Grafana               | http://localhost:3000      | admin / admin                  |
| Prometheus            | http://localhost:9090      | —                              |
| Pyroscope             | http://localhost:4040      | —                              |
| Quickwit (log index)  | http://localhost:7280      | —                              |
| Neo4j Browser         | http://localhost:7474      | see `docker-compose.yml`       |
| RabbitMQ Mgmt         | http://localhost:15672     | pulsetrace / pulsetrace_secret |
| MinIO Console         | http://localhost:9001      | minioadmin / minioadmin        |
| ClickHouse HTTP (SQL) | http://localhost:8123      | default / (no password)        |

---

## Event Flow

### Log Ingestion Pipeline

```
POST /api/v1/logs
      │
      ▼
gateway-service
  └─ injects W3C traceparent header
      │
      ▼
log-service
  ├─ validates log entry
  ├─ starts OTel span (child of gateway span)
  └─ publishes to Kafka "logs" topic (with trace headers)
                    │
          ┌─────────┴──────────┐
          ▼                    ▼
  alert-service          Quickwit Kafka source
  consumer                 ├─ VRL transform (JSON passthrough, grok for syslog)
  ├─ extracts              ├─ dynamic schema — arbitrary fields are indexed
  │  trace context         └─ indexes into "pulsetrace-logs" (splits on disk/S3)
  ├─ level == ERROR/FATAL?                    │
  │     YES → insert alert into PostgreSQL    │
  └─ publish to Kafka "alerts" topic          ▼
                    │              log search (Explorer) · SLI / error-budget
                    ▼
        correlation-service
          ├─ extracts trace context
          ├─ finds open incident in 5-min window
          │     found  → add alert to existing incident
          │     not found → create new incident with root-cause inference
          └─ publishes NotificationEvent to RabbitMQ
                              │
                              ▼
                notification-service consumer
                  ├─ logs structured notification (always)
                  ├─ posts to Slack (if SLACK_WEBHOOK_URL set)
                  ├─ sends email (if SMTP_HOST set)
                  ├─ pages PagerDuty (if PAGERDUTY_ROUTING_KEY set)
                  ├─ opens an Opsgenie alert (if OPSGENIE_API_KEY set)
                  └─ POSTs a signed webhook (if WEBHOOK_URL set)
```

Every channel is independent and opt-in: set its env var to activate it, leave
it blank to skip it. PagerDuty and Opsgenie deduplicate on the incident ID, so
repeated notifications for one incident coalesce into a single alert rather than
paging on-call once per update. The generic webhook signs each request with
`X-PulseTrace-Signature: sha256=<hmac>` when `WEBHOOK_SECRET` is set.

### SLO Burn Rate Engine

```
[background goroutine — every 60 seconds]
      │
      ▼
SLOWorker.tick()
  └─ ListDefinitions() from PostgreSQL
        │
        ▼  for each SLO definition:
  ComputeSLI() ← Quickwit aggregation over "pulsetrace-logs"
    total vs. level IN ('ERROR','FATAL'), filtered by service + time window
    (falls back to PostgreSQL when QUICKWIT_URL is unset)
        │
        ├─ InsertSnapshot() → PostgreSQL (SLI history)
        └─ BurnRateAlerter.Evaluate()
              ├─ budget remaining > 50%  → status: healthy
              ├─ budget remaining < 50%  → status: warning  → RabbitMQ alert
              └─ budget remaining < 10%  → status: critical → RabbitMQ alert
```

---

## API Reference

All endpoints are proxied through the gateway at `http://localhost:8080`.

**164 routes** are live. The sections below cover the core incident path; the
complete, always-current surface is generated into
[PARITY_REPORT.md](PARITY_REPORT.md) by CI. The other route groups:

| Group | Prefix | Covers |
| --- | --- | --- |
| Traces | `/api/v1/traces`, `/api/v1/analytics/traces` | Search, facets, latency distribution, trace detail |
| Metrics | `/api/v1/metrics` | Names, catalog/labels, query (`rate`, `p50–p99`), multi-series formulas |
| Errors | `/api/v1/errors/groups` | Grouping, similar-issue clustering, timeline, triage (resolve/mute/reopen) |
| RUM | `/api/v1/rum` | Ingest, sessions, web vitals, trends, devices, errors |
| Synthetics | `/api/v1/synthetics` | Tests CRUD, results, uptime/SLA |
| Profiler | `/api/v1/profiler` | Flat profile, functions, regression diff |
| Topology | `/api/v1/topology` | Graph, upstream/downstream (blast radius), catalog |
| Deploys | `/api/v1/deployments` | Markers, gates, DORA, change-failure linking |
| SLO | `/api/v1/slo` | Definitions, dashboard, budget alerts, PR evaluation |
| Remediation | `/api/v1/incidents/{id}/playbook` | Dry-run, approve, reject |
| AI-SRE | `/api/v1/chat` | Streaming copilot, tool-call transparency, suggestions |
| Auth | `/api/v1/auth`, `/scim/v2` | Login, OIDC, SAML, SCIM, MFA, sessions, password lifecycle |
| Admin | `/api/v1/admin` | Users, roles, ABAC policies, rate limits, ingestion keys, audit log, tenant |
| Billing | `/api/v1/billing`, `/api/v1/usage` | Plans, checkout, portal, invoices, usage vs quota |

### Logs

| Method | Path                        | Description                            |
|--------|-----------------------------|----------------------------------------|
| POST   | `/api/v1/logs`              | Ingest a structured log event          |
| GET    | `/api/v1/logs`              | Search logs (Quickwit-backed)          |
| GET    | `/api/v1/logs/{id}`         | Get a single log by ID                 |
| GET    | `/api/v1/logs/{id}/context` | Surrounding logs from the same service |

**POST body:**
```json
{
  "service":  "payment-service",
  "level":    "ERROR",
  "message":  "database timeout",
  "trace_id": "abc-123",
  "span_id":  "def-456",
  "metadata": { "region": "us-east-1" }
}
```

**GET query params:** `service`, `level`, `trace_id`, `q` (phrase match on the
message), `regex` (full regex on the message), `start` / `end` (RFC3339),
`since` (relative, e.g. `15m`, `2h`, `7d` — an explicit `start` wins), `limit`
(default 100, max 1000).

`/context` takes `before` and `after` (default 25/side, max 200); the anchor log
is resolved server-side, so the caller never supplies its service or timestamp.

---

### Alerts

| Method | Path                   | Description              |
|--------|------------------------|--------------------------|
| GET    | `/api/v1/alerts`       | List alerts (filterable) |
| GET    | `/api/v1/alerts/{id}`  | Get a single alert       |

**GET query params:** `service`, `level`, `from`, `to`, `page`, `page_size`

---

### Incidents

| Method | Path                              | Description                          |
|--------|-----------------------------------|--------------------------------------|
| GET    | `/api/v1/incidents`               | List incidents with root cause       |
| GET    | `/api/v1/incidents/{id}`          | Get a single incident                |
| GET    | `/api/v1/incidents/{id}/timeline` | Ordered event timeline for incident  |

**GET query params:** `status` (OPEN/RESOLVED), `severity`, `service`, `from`, `to`, `page`, `page_size`

**Example incident response:**
```json
{
  "id": "e4661798-...",
  "title": "[ERROR] payment-service degradation detected",
  "root_cause": "Database or network connectivity issue",
  "status": "OPEN",
  "severity": "ERROR",
  "services": ["payment-service"],
  "alert_count": 3,
  "started_at": "2026-05-16T13:45:41Z"
}
```

**Example timeline response:**
```json
[
  { "at": "13:45:41", "event_type": "incident_opened",  "description": "Incident opened: [ERROR] payment-service degradation detected" },
  { "at": "13:45:41", "event_type": "alert_triggered",  "service": "payment-service", "level": "ERROR", "description": "[ERROR] payment-service: DB connection pool exhausted (attempt 1)" },
  { "at": "13:45:42", "event_type": "alert_triggered",  "service": "payment-service", "level": "ERROR", "description": "[ERROR] payment-service: DB connection pool exhausted (attempt 2)" },
  { "at": "13:45:43", "event_type": "alert_triggered",  "service": "payment-service", "level": "ERROR", "description": "[ERROR] payment-service: DB connection pool exhausted (attempt 3)" }
]
```

---

### SLO / Error Budget

All SLI values are computed from the live Quickwit `pulsetrace-logs` index
(with a PostgreSQL fallback when Quickwit is not configured).

| Method | Path                            | Description                                      |
|--------|---------------------------------|--------------------------------------------------|
| POST   | `/api/v1/slo/definitions`       | Create or update an SLO target for a service     |
| GET    | `/api/v1/slo/definitions`       | List all configured SLO definitions              |
| DELETE | `/api/v1/slo/definitions/{id}`  | Delete an SLO definition                         |
| GET    | `/api/v1/slo/dashboard`         | Full dashboard: SLI, burn rate, error budget     |
| GET    | `/api/v1/slo/budget-alerts`     | Recent burn rate breach events                   |

**POST `/api/v1/slo/definitions` body:**
```json
{
  "service_name": "payment-service",
  "slo_target":   99.9,
  "sli_type":     "availability",
  "window_days":  30
}
```

**Example dashboard response:**
```json
{
  "success": true,
  "data": [{
    "definition": {
      "service_name": "payment-service",
      "slo_target":   99.9,
      "window_days":  30
    },
    "current_sli":        98.5,
    "total_events":       10000,
    "error_events":       150,
    "budget_total_min":   43.2,
    "budget_used_min":    21.6,
    "budget_remaining_pct": 50.0,
    "burn_rate":          0.5,
    "status":             "warning",
    "trend": [
      { "at": "2026-06-07T03:00:00Z", "sli_value": 99.1 },
      { "at": "2026-06-07T04:00:00Z", "sli_value": 98.5 }
    ]
  }]
}
```

---

## Root Cause Inference

The correlation engine scans alert messages for known patterns and maps them to probable root causes:

| Pattern detected | Inferred root cause |
|------------------|---------------------|
| `connection`     | Database or network connectivity issue |
| `timeout`        | Downstream service latency or resource exhaustion |
| `memory`         | Memory pressure — possible OOM condition |
| `kafka`          | Kafka broker unavailability or consumer lag |
| `auth`           | Authentication service degradation |
| `permission`     | Authorization failure or misconfigured credentials |
| `crash`          | Application panic or unhandled exception |
| `unavailable`    | Upstream service is down or unreachable |

Alerts from the same service within a **5-minute sliding window** are grouped into a single incident. The incident's `alert_count` increments with each new alert, and `severity` is automatically promoted to the highest level seen.

---

## Beyond Correlation: Causal AI

Pattern matching tells you *what kind* of error happened. **Causal AI** answers a fundamentally harder question: *what caused what*.

When an incident is created or updated, the correlation service asynchronously:

1. **Builds a deterministic causal chain** by walking the declared service dependency graph in temporal order — for each alert, it finds the earliest preceding alert from a known upstream service and emits a causal edge.
2. **Hands the chain + alerts + dependency graph to Claude** (via the Anthropic Messages API with prompt caching) to refine the hypothesis, produce a confidence score, and narrate the causal story in plain English.
3. **Persists the result** to the incident row as JSONB and surfaces it on the incident API + timeline.

If `ANTHROPIC_API_KEY` is not set, the service falls back to the deterministic chain alone (no narrative, no confidence refinement) — **everything keeps working without the LLM**.

### Architecture

```
Kafka "alerts" topic
       │
       ▼
correlation-service.Handle
       │
       ├─→ repo.Upsert(incident, alert)         ← synchronous, fast
       │
       └─→ scheduleCausalAnalysis(incident.id)  ← async, deduped per-incident
                                  │
                                  ▼
                    causal.Analyzer.Analyze(evidence)
                                  │
                  ┌───────────────┴───────────────┐
                  ▼                               ▼
        NoopAnalyzer (default)         ClaudeAnalyzer (if API key)
        BuildChain from deps           BuildChain + LLM refinement
                  │                               │
                  └───────────────┬───────────────┘
                                  ▼
                    repo.UpdateCausalAnalysis(jsonb)
```

### Example output

```json
GET /api/v1/incidents/{id}

{
  "id": "e4661798-...",
  "title": "[ERROR] payment-service degradation detected",
  "root_cause": "Database or network connectivity issue",
  "severity": "ERROR",
  "alert_count": 3,
  "services": ["payment-service", "postgres", "auth-service"],
  "causal": {
    "chain": [
      {
        "from_service": "postgres",
        "to_service": "payment-service",
        "evidence": "postgres connection pool exhausted at 13:45:38 preceded payment-service timeouts at 13:45:40; declared dependency",
        "at": "2026-05-16T13:45:38Z"
      },
      {
        "from_service": "payment-service",
        "to_service": "order-service",
        "evidence": "payment-service errors at 13:45:41 preceded order-service failures at 13:45:43",
        "at": "2026-05-16T13:45:41Z"
      }
    ],
    "narrative": "The incident originated in postgres at 13:45:38 with connection pool exhaustion, which caused payment-service to time out on queries starting at 13:45:40. The failure then propagated to order-service, which depends on payment-service, at 13:45:43. Recommend checking postgres connection limits and active query load.",
    "root_cause": "Postgres connection pool exhaustion — likely runaway query or insufficient pool size for current load.",
    "confidence": 0.87,
    "model": "claude-opus-4-7",
    "analyzed_at": "2026-05-16T13:45:44Z"
  }
}
```

### Why this is interesting

Most "AI for observability" features bolt an LLM onto raw logs and hope for the best. PulseTrace's approach is different and more honest:

- **Deterministic first, LLM second.** The causal chain is computed by graph traversal — no LLM required, no hallucination possible. The LLM only *refines and narrates* a chain it didn't invent.
- **Grounded in declared dependencies.** The model is given the explicit service dependency graph as cached context, so it can't reference services that don't exist.
- **Prompt caching.** The static system prompt + dependency graph (~1 KB) is cached via `cache_control: ephemeral`, so each subsequent incident analysis pays ~10% of the first call's input cost.
- **Confidence is mandatory.** The model must return a 0.0–1.0 score; low-evidence incidents are flagged honestly rather than hidden behind authoritative-sounding prose.
- **Graceful degradation.** No API key → still produces a causal chain. API call fails → falls back to the rule-based analyzer automatically. The incident pipeline never blocks on the LLM.

### Configuration

| Env var                            | Default            | Description                                                                                                    |
|------------------------------------|--------------------|----------------------------------------------------------------------------------------------------------------|
| `LLM_PROVIDERS`                    | *(unset)*          | Ordered failover chain, e.g. `anthropic:claude-sonnet-4-5,openai:gpt-4o-mini,ollama:llama3`. Used by both the causal analyzer and the chat/SLO handlers. |
| `LLM_PROVIDER` / `LLM_MODEL`       | *(unset)*          | Legacy single-provider form. Ignored when `LLM_PROVIDERS` is set.                                               |
| `ANTHROPIC_API_KEY`                | *(unset)*          | If no provider is configured at all, the rule-based `NoopAnalyzer` is used.                                    |
| `OPENAI_API_KEY`, `GEMINI_API_KEY` | *(unset)*          | Credentials for the corresponding providers. A provider whose key is missing is skipped at startup.            |
| `OPENAI_BASE_URL`, `OLLAMA_ENDPOINT` | *(unset)*        | Per-provider endpoint overrides. Prefer these over the shared `LLM_ENDPOINT`, which is ambiguous when chaining. |
| `CAUSAL_DISABLED`                  | *(unset)*          | Set to `true` to force the noop analyzer even if a provider is configured.                                     |

Providers in a chain are tried in order. One that errors is skipped for a
cooldown window (30s, doubling per consecutive failure) so a hard-down provider
doesn't add its timeout to every incident — but it is never permanently
evicted, and if all providers are cooling down the chain retries them anyway.
A failed chain still falls back to the deterministic `NoopAnalyzer`, so the
incident pipeline never blocks on an LLM.

---

## Self-Healing & the Approval Gate

When causal AI proposes a recovery playbook, whether PulseTrace may act on it
is a policy decision, not a confidence score. `REMEDIATION_MODE` governs it,
and **every service in the chain enforces it independently** — correlation-service
decides, topology-service and action-service execute, and each one can veto. An
on-prem agent pinned to `dry-run` stays dry-run no matter what the control
plane asks of it.

| Mode      | Behaviour                                                                          |
|-----------|------------------------------------------------------------------------------------|
| `off`     | Never executes. Playbooks are recorded as suggestions only.                        |
| `dry-run` | Plans the remediation and records exactly what would run. Changes nothing.         |
| `manual`  | **Default.** Requires a human to approve each remediation before it executes.      |
| `auto`    | Executes unattended once confidence clears `REMEDIATION_CONFIDENCE_THRESHOLD` (0.70). |

An unrecognized value is rejected and the service falls back to `manual` rather
than failing open. The local `docker-compose` stack sets `auto` so the demo
still shows remediation end-to-end; the Kubernetes manifests set `manual`.

Approval endpoints (proxied through the gateway, tenant-scoped, and attributed
to the gateway-verified user — never to a name supplied in the request body):

```
GET  /api/v1/remediation/policy                  # current mode, for the UI
POST /api/v1/incidents/{id}/playbook/dry-run     # show what it would do
POST /api/v1/incidents/{id}/playbook/approve     # authorize and execute
POST /api/v1/incidents/{id}/playbook/reject      # decline, with an optional reason
```

The approver's identity and timestamp are recorded on the playbook *before*
execution begins, so "who authorized this" stays answerable even if the run
itself crashes. The `dry_run` flag is covered by the HMAC that signs agent
requests, so a captured dry-run request can't be replayed as a live change.

---

## Distributed Tracing

Every request carries a W3C `traceparent` header through the entire call chain:

```
gateway-service: POST /api/v1/logs          ← root span
  └── log-service: POST /api/v1/logs        ← child span (HTTP propagation)
        ├── log.ingest                       ← handler span
        ├── db.insert_log                    ← DB span
        └── kafka.publish_log               ← Kafka publish span (headers injected)
              └── alert-service: alert.process_log_event   ← consumer span (headers extracted)
                    ├── db.insert_alert     ← DB span
                    └── kafka.publish_alert ← Kafka publish span
                          └── correlation-service: correlation.process_alert
                                ├── db.upsert_incident
                                └── rabbitmq.publish_notification
```

View traces at **http://localhost:16686** — select any service and click "Find Traces".

---

## Zero-code migration (Datadog / Splunk)

Point your **existing** Datadog agent or Splunk forwarder at PulseTrace by
changing only the ingestion URL — no re-instrumentation. The gateway accepts the
native wire formats, **authenticates them with a PulseTrace ingestion key**
(carried in each protocol's own auth header), translates to OTLP, and stamps the
tenant — so migrated telemetry lands with the same isolation as native OTLP.

> This is an authenticating, tenant-attributing front door in the gateway
> ([`internal/ingestproxy`](gateway-service/internal/ingestproxy/)). The
> collector's own Datadog/Splunk receivers — which do neither — are **not**
> exposed; agents point at the gateway, never the collector.

**Datadog** — send traces to the gateway and set your PulseTrace ingestion key as
the API key:

```yaml
# Datadog Agent (datadog.yaml) or a DD tracing library
apm_config:
  apm_dd_url: "http://<gateway-host>:8080"   # was https://trace.agent.datadoghq.com
api_key: "pt_ingest_<your-pulsetrace-ingestion-key>"
```

Datadog endpoints (all authenticated by `DD-API-KEY`, gzip/deflate bodies
handled), each translated to OTLP and tenant-stamped:

| Signal | Endpoint | Notes |
| --- | --- | --- |
| Traces | `POST /v0.3\|v0.4\|v0.5/traces` | JSON and msgpack, incl. v0.5 string-table |
| Metrics | `POST /api/v1/series`, `POST /api/v2/series` | gauge→Gauge, count→monotonic Sum |
| Logs | `POST /api/v2/logs` | `ddtags` → attributes |

**Splunk** — send HEC events/metrics to the gateway with your ingestion key as
the HEC token:

```bash
curl http://<gateway-host>:8080/services/collector/event \
  -H "Authorization: Splunk pt_ingest_<your-pulsetrace-ingestion-key>" \
  -d '{"event":"hello from splunk","sourcetype":"app","fields":{"env":"prod"}}'
```

Splunk endpoints: `POST /services/collector`, `/services/collector/event`,
`/services/collector/raw`. HEC **metric** events (fields carrying a
`metric_name`) are routed to the metrics pillar; everything else is a log.

Traces and metrics land in ClickHouse (`otel_traces` / `otel_metrics_*`) with the
resolved `tenant.id` resource attribute. **Migration logs** join the product's
native log path — the gateway publishes them to Kafka as `LogEntry` records, so
Quickwit indexes them into the `pulsetrace-logs` index and they appear in the
**log explorer UI** alongside native logs, scoped by `tenant_id` (metered and
quota-checked like native ingestion). If the broker is unreachable at startup the
gateway falls back to forwarding logs to ClickHouse `otel_logs` via OTLP. Either
way, migrated telemetry is tenant-isolated exactly like native OTLP — verified
end-to-end.

Timestamps are normalized by magnitude, not by trusting each protocol's nominal
unit — so a misconfigured agent that sends epoch-seconds where millis are expected
(or vice versa) still lands its telemetry at the right time instead of in 1970 or
the far future. Absent timestamps default to receive time; genuinely old
timestamps (backfill) are preserved.

Manage keys under `/api/v1/admin/ingestion-keys`: `POST` mints, `GET` lists
(metadata only, never the secret), `DELETE /{id}` revokes immediately, and
`POST /{id}/rotate` rotates. Rotation mints a replacement inheriting the key's
tenant/tier/scope and revokes the old one after a grace window
(`{"grace_period":"24h"}`, default 24h, `"0"` = immediate) — so a public RUM token
embedded in already-served browser pages keeps working until clients pick up the
new one, instead of breaking the instant the key changes. The old key links to its
successor (`replaced_by`) for lineage.

Mint keys with `POST /api/v1/admin/ingestion-keys`. When `REQUIRE_INGESTION_KEY`
is unset (dev), un-keyed migration traffic is accepted as the `default` tenant;
set it to `"true"` for any multi-tenant deployment so a key is mandatory. A
RUM-scoped key is rejected here — these paths only accept server-ingest keys.

---

## Running Locally (without Docker)

Easiest path: run the infrastructure from compose and the Go services on the host.

```bash
# 1. Infrastructure only (Postgres, Kafka, RabbitMQ, Quickwit, ClickHouse, Neo4j, Redis)
docker compose up -d postgres kafka zookeeper rabbitmq quickwit clickhouse neo4j redis

# 2. Point the services at it. Migrations are embedded and applied at boot by
#    shared/migrate — there is no manual psql step.
export DATABASE_URL="postgres://pulsetrace:pulsetrace_secret@localhost:5434/pulsetrace?sslmode=disable"
export KAFKA_BROKERS="localhost:9092"
export RABBITMQ_URL="amqp://pulsetrace:pulsetrace_secret@localhost:5672/"
export QUICKWIT_URL="http://localhost:7280"
export CLICKHOUSE_URL="http://localhost:8123"
export OTEL_EXPORTER_OTLP_ENDPOINT="localhost:4317"

# 3. Run each service in its own terminal
cd log-service          && go run ./cmd     # :8081
cd alert-service        && go run ./cmd     # :8082
cd correlation-service  && go run ./cmd     # :8083
cd topology-service     && go run ./cmd     # :8084
cd action-service       && go run ./cmd     # :8085
cd notification-service && go run ./cmd     # :8086
cd gateway-service      && LOG_SERVICE_URL=http://localhost:8081 \
                           ALERT_SERVICE_URL=http://localhost:8082 \
                           CORRELATION_SERVICE_URL=http://localhost:8083 \
                           TOPOLOGY_SERVICE_URL=http://localhost:8084 \
                           NOTIFICATION_SERVICE_URL=http://localhost:8086 \
                           go run ./cmd     # :8080

# 4. Dashboard
cd frontend && npm run dev
```

Postgres is published on **5434** (not 5432) to avoid colliding with a local
install. [.env.example](.env.example) documents the optional integrations
(SSO, SMTP, PagerDuty, Opsgenie, webhooks, remediation policy).

Two secrets **fail closed** and are not yet in `.env.example` — set them or the
feature refuses to start rather than storing plaintext:
`CHANNEL_ENCRYPTION_KEY` (notification-channel secrets) and `MFA_ENCRYPTION_KEY`
(TOTP secrets), both 32-byte AES-256 keys.

---

## Kubernetes Deployment

Manifests are in `k8s/`. Requires a running cluster with an nginx ingress controller.

```bash
# Apply in order
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/configmap.yaml
# secret.yaml is git-ignored — see k8s/README.md. For dev:
#   cp k8s/secret.yaml.example k8s/secret.yaml && edit values
# For prod, sync from a secret backend via k8s/externalsecret.yaml.example.
kubectl apply -f k8s/secret.yaml        # update values before applying
kubectl apply -f k8s/log-service.yaml
kubectl apply -f k8s/alert-service.yaml
kubectl apply -f k8s/correlation-service.yaml
kubectl apply -f k8s/notification-service.yaml
kubectl apply -f k8s/gateway.yaml

# Check rollout
kubectl rollout status deployment -n pulsetrace

# Access via ingress (add pulsetrace.local to /etc/hosts)
curl http://pulsetrace.local/api/v1/logs
```

Each service has:
- `livenessProbe` and `readinessProbe` on `/healthz`
- `HorizontalPodAutoscaler` (CPU 70% target, 2–10 replicas)
- Resource requests and limits
- Config from ConfigMap / Secrets (no hardcoded values)

---

## Project Structure

Eight Go modules (`go.work`) plus the Next.js frontend.

```
pulsetrace/
├── gateway-service/           # Front door: proxy, auth, OTLP, ingest proxy, most product APIs
│   ├── internal/handler/      # Traces, metrics, RUM, synthetics, errors, profiler, deploys, usage
│   ├── internal/auth/         # OIDC, SAML, SCIM, MFA/TOTP, sessions, RBAC/ABAC, audit chain
│   ├── internal/ingestproxy/  # Datadog + Splunk HEC decoders → OTLP
│   ├── internal/otlp/         # OTLP receiver + tenant resolution
│   ├── internal/logbridge/    # OTLP logs → native LogEntry → Kafka
│   ├── internal/billing/ quota/ metering/ pii/ tenantdata/
│   └── migrations/            # 001–025 (Postgres, embedded + applied at boot)
├── log-service/               # Log ingest → Kafka; search API over Quickwit
│   ├── internal/handler/      # Query, single log, surrounding context
│   └── migrations/            # Postgres log_entries (fallback path)
├── alert-service/             # Kafka consumer → alerts → re-publish; silences
├── correlation-service/       # Incidents, causal RCA, SLOs, anomalies, postmortems, AI-SRE chat
│   ├── internal/engine/       # Correlator, SLO worker, burn-rate alerter, forecasting, anomalies
│   ├── internal/handler/      # Incidents, SLO, playbook approval, chat, suggestions
│   └── migrations/            # 001–006
├── topology-service/          # Service graph + catalog (Neo4j), OTLP traces receiver
├── action-service/            # Executes approved remediation playbooks (HMAC-verified)
├── notification-service/      # RabbitMQ → Slack / email / PagerDuty / Opsgenie / webhook
│   └── internal/channels/     # Per-tenant channels, AES-256-GCM secrets at rest
├── shared/                    # db · kafka · rabbitmq · models · middleware · telemetry
│   ├── causal/                # Causal analyzer, grounding guardrail, eval harness + fixtures
│   ├── remediation/           # Policy modes + risk-tier authorization
│   └── metering/ jsonpool/ migrate/ client/
├── frontend/                  # Next.js dashboard (17 screens)
│   ├── src/app/               # Routes
│   ├── src/components/        # One directory per feature + ui/ primitives
│   ├── src/lib/api/           # Typed client (ApiError, envelopes)
│   └── tests/e2e/             # Playwright, incl. a11y scan
├── quickwit/                  # Log index config + Kafka source (VRL transform)
├── clickhouse/storage.xml     # Tiered storage policy (S3 / Azure / GCS)
├── otel-collector/ prometheus/ grafana/ vector/
├── scripts/
│   ├── load/                  # k6 ingestion harness → PERF_BASELINE.md
│   └── parity/                # Backend↔UI parity gate → PARITY_REPORT.md
├── docs/                      # Enhancement specs + implementation plans + competitive plan
├── helm/ k8s/                 # Chart + manifests (HPA, PDB, probes, ExternalSecret)
├── docker-compose.yml · go.work · README.md
```

---

## Tech Stack

| Area                  | Technology                                                |
|-----------------------|-----------------------------------------------------------|
| Language              | Go 1.24 (8 modules via `go.work`)                         |
| API                   | `net/http` (stdlib)                                       |
| **Log store**         | **Quickwit — dynamic schema, Kafka source, splits on disk/object store** |
| Telemetry store       | ClickHouse (MergeTree) — traces, metrics, RUM, synthetics  |
| Graph store           | Neo4j — service dependencies + catalog                    |
| Cold archival         | AWS S3 / Azure Blob / GCP GCS (via ClickHouse tiered policy) |
| Local emulators       | MinIO (S3), Azurite (Azure Blob)                          |
| Relational DB         | PostgreSQL 16 (control plane: incidents, SLOs, auth, billing, audit) |
| Cache                 | Redis (ingestion keys, sessions)                          |
| Message broker        | Apache Kafka (Sarama)                                     |
| Notification queue    | RabbitMQ 3.13 (amqp091-go) with DLQ                       |
| Distributed tracing   | OpenTelemetry SDK + Jaeger + ClickHouse                   |
| Continuous profiling  | Grafana Pyroscope                                         |
| Metrics               | Prometheus + Grafana                                      |
| Frontend              | React 19, Next.js, TypeScript                             |
| Causal AI             | LangChain Go (Anthropic Claude / OpenAI / Gemini / Ollama)|
| Auth                  | OIDC, SAML 2.0 (`crewjam/saml`), SCIM 2.0, TOTP MFA, RBAC + ABAC (`expr-lang`) |
| Testing               | Go table-driven + DB-backed, Playwright e2e, `@axe-core` a11y, k6 load |
| Containers            | Docker + Compose                                          |
| Orchestration         | Kubernetes (Deployments, HPA, PDB, Ingress) + Helm        |

---

## Status

| Gate | State | Evidence |
| --- | --- | --- |
| Backend ↔ UI parity | **100%** — 164 routes, 0 orphans, enforced in CI | [PARITY_REPORT.md](PARITY_REPORT.md) |
| Causal-AI accuracy | **90.9%** rule-based, CI-gated (≥85% overall, ≥95% playbook) | `shared/causal/eval_test.go` |
| Ingestion performance | p95/p99 per protocol, gated per PR | [PERF_BASELINE.md](PERF_BASELINE.md) |
| Tenant isolation | Fail-closed ClickHouse read guard + static ratchet | [TENANT_ISOLATION.md](TENANT_ISOLATION.md) |
| Security scanning | `govulncheck` at zero call-reachable CVEs across 8 modules | CI `security` job |
| DR posture | RPO/RTO per tier, ordered restore runbooks | [DISASTER_RECOVERY.md](DISASTER_RECOVERY.md) |

**Delivered** — Waves 1–4 of [ROAD_TO_100.md](ROAD_TO_100.md): parity gate, load
harness, structural tenant isolation, typed frontend platform, causal-eval
harness; every backend-without-UI orphan closed; pillar depth across logs,
traces, metrics, RUM, synthetics, errors, profiling and topology; and the
revenue/enterprise surface (billing + dunning, MFA, SAML, SCIM, session
revocation, guided ABAC, tamper-evident audit, Helm/GHCR deploy).

Since then, per-pillar enhancements (see the git log for the delivering commits):
AI-SRE tool transparency + citations, AI postmortems, alert grouping/dedup +
silences, multi-window burn-rate alerting, DORA + change-failure linking,
histogram brush-to-zoom, trace latency distribution, diff flame graphs,
similar-issue clustering, session timelines, and anomaly overlay on the topology
graph.

**Next** — closing the substrate gap against OpenObserve (single-container
deployment, object-store-primary storage, a real query language, dashboards,
pipelines, ingestion breadth) while widening the incident-intelligence lead:
[docs/COMPETITIVE_OPENOBSERVE.md](docs/COMPETITIVE_OPENOBSERVE.md) and its
[phase-wise implementation plan](docs/COMPETITIVE_OPENOBSERVE_IMPLEMENTATION.md).

---

## Documentation

| Document | What it is |
| --- | --- |
| [ROAD_TO_100.md](ROAD_TO_100.md) | The delivered-work record — what shipped and why |
| [docs/COMPETITIVE_OPENOBSERVE.md](docs/COMPETITIVE_OPENOBSERVE.md) | Gap analysis vs OpenObserve + the plan to win each dimension |
| [docs/COMPETITIVE_OPENOBSERVE_VERIFICATION.md](docs/COMPETITIVE_OPENOBSERVE_VERIFICATION.md) | Source-verified findings on OpenObserve, with file citations |
| [docs/COMPETITIVE_OPENOBSERVE_IMPLEMENTATION.md](docs/COMPETITIVE_OPENOBSERVE_IMPLEMENTATION.md) | The phase-wise implementation plan |
| [TENANT_ISOLATION.md](TENANT_ISOLATION.md) | How every store is tenant-scoped, with the honest limitations |
| [DISASTER_RECOVERY.md](DISASTER_RECOVERY.md) | Backup strategy, restore runbooks, drill cadence |
| [PARITY_REPORT.md](PARITY_REPORT.md) | Generated by CI — backend↔UI parity |
| [PERF_BASELINE.md](PERF_BASELINE.md) | Machine-written ingestion performance baseline |
| [k8s/README.md](k8s/README.md) · [helm/pulsetrace/README.md](helm/pulsetrace/README.md) | Deployment |
| [scripts/parity/README.md](scripts/parity/README.md) · [scripts/load/README.md](scripts/load/README.md) | The CI gates |
