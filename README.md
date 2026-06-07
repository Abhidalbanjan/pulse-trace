# PulseTrace — Distributed Observability & Incident Monitoring Platform

A production-grade, event-driven observability platform for microservices, built entirely in Go.

> Think mini-Datadog / mini-Grafana — built from scratch, with analytical-grade telemetry storage, multi-cloud cold tiering, and AI-driven root cause analysis.

---

## Architecture

```
  OTel Agents / SDKs
  OTLP/gRPC :4317 ─────────────────────────────────────────────────────────────────────┐
  OTLP/HTTP  :8080/v1/* ──────────────────────────────┐                                │
                                                       │                                │
                        ┌──────────────────────────────▼──────────────────────────┐    │
                        │     API Gateway  :8080                                   │    │
                        │  (reverse proxy · W3C tracing · OTLP passthrough · RBAC)│    │
                        └──┬──────────┬─────────────┬────────────────────────┬────┘    │
                           │          │              │                        │         │
                    ┌──────▼──┐  ┌────▼────┐  ┌─────▼──────────┐            │         │
                    │  Log    │  │  Alert  │  │  Correlation    │            │         │
                    │ Service │  │ Service │  │    Service      │            │         │
                    │  :8081  │  │  :8082  │  │     :8083       │            │         │
                    └────┬────┘  └────┬────┘  └────────┬────────┘            │         │
                         │            │            SLO Worker                │         │
                    Kafka "logs"  Kafka "alerts"  (every 60s)           OTel Collector │
                         │            │                 │                     │◄────────┘
                         │            │                 ▼                     │
                         │            │       ┌─────────────────┐             ▼
                         │            │       │  ClickHouse SLI │        Jaeger / Prom
                         │            │       │  Query (native) │
                         │            │       └─────────────────┘
                         │            │                 │
                         │            │           RabbitMQ
                         │            │                 │
                         └────────────┘                 ▼
                                  │              Notification
                                  │                Service
                   ┌──────────────▼──────────────────────────┐
                   │       Kafka Batch Consumer (Go)          │
                   │  buffer ≤10k logs · flush every 100ms    │
                   └──────────────┬──────────────────────────┘
                                  │
                   ┌──────────────▼──────────────────────────┐
                   │       ClickHouse  (hot SSD tier)         │
                   │  MergeTree · partitioned toYYYYMM(ts)    │
                   └──────────────┬──────────────────────────┘
                                  │  tiered storage policy
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
        AWS S3 / MinIO    Azure Blob / Azurite    GCP GCS
        (s3_cold disk)    (azure_cold disk)    (gcp_cold disk)

  PostgreSQL ← alerts, incidents, SLO definitions, SLI snapshots

  ┌─────────────────────────────────────────────────────────────────┐
  │                    Observability Stack                          │
  │  OTel Collector → Jaeger (traces)  ·  Prometheus → Grafana     │
  └─────────────────────────────────────────────────────────────────┘
```

---

## What's Built

| Component                  | Responsibility                                                                          |
|----------------------------|-----------------------------------------------------------------------------------------|
| `gateway-service`          | Reverse proxy, W3C trace context propagation, OTLP gRPC/HTTP passthrough, RBAC         |
| `log-service`              | Ingest logs → publish to Kafka; batch consumer writes to ClickHouse; serves query API  |
| `alert-service`            | Consume `logs` topic, create alerts for ERROR/FATAL, publish to `alerts` topic          |
| `correlation-service`      | Consume `alerts`, group into incidents, infer root cause, SLO burn rate engine          |
| `notification-service`     | Consume RabbitMQ, dispatch to Slack / email / log                                       |
| `shared`                   | Models, DB pools (Postgres + ClickHouse), Kafka, RabbitMQ, OTel middleware              |
| ClickHouse                 | Column-oriented analytical store for high-volume logs (hot SSD tier)                   |
| MinIO                      | Local S3-compatible emulator for ClickHouse cold tier (dev/test)                       |
| Azurite                    | Local Azure Blob Storage emulator for ClickHouse cold tier (dev/test)                  |
| PostgreSQL                 | Persistent storage for alerts, incidents, SLO definitions, and SLI snapshots           |
| Kafka                      | Event bus: `logs` and `alerts` topics                                                   |
| RabbitMQ                   | Notification pipeline with dead-letter queue                                            |
| OTel Collector             | Receives OTLP spans from all services, forwards to Jaeger                               |
| Jaeger                     | Distributed trace visualization                                                         |
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

# 5. Query logs (served from ClickHouse)
curl "http://localhost:8080/api/v1/logs?service=payment-service&level=ERROR"

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

# 11. Move a partition to cold storage manually (ClickHouse CLI)
docker exec -it pulsetrace-clickhouse-1 clickhouse-client \
  --query "ALTER TABLE logs MOVE PARTITION '$(date +%Y%m)' TO DISK 's3_cold'"
```

---

## Observability UIs

| UI                   | URL                        | Credentials                    |
|----------------------|----------------------------|--------------------------------|
| Jaeger               | http://localhost:16686     | —                              |
| Grafana              | http://localhost:3000      | admin / admin                  |
| Prometheus           | http://localhost:9090      | —                              |
| RabbitMQ Mgmt        | http://localhost:15672     | pulsetrace / pulsetrace_secret |
| MinIO Console        | http://localhost:9001      | minioadmin / minioadmin        |
| ClickHouse HTTP (SQL)| http://localhost:8123      | default / (no password)        |

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
  alert-service        ClickHouse Batch Consumer (log-service)
  consumer               ├─ buffers messages in-memory channel
  ├─ extracts            ├─ flushes every 100ms or 10,000 entries
  │  trace context       └─ transactional BulkInsert → ClickHouse (hot SSD tier)
  ├─ level == ERROR/FATAL?                    │
  │     YES → insert alert into PostgreSQL    │ tiered storage policy
  └─ publish to Kafka "alerts" topic    ┌─────┴──────────────┐
                    │                   ▼                    ▼
                    ▼             AWS S3 / MinIO      Azure Blob / GCS
        correlation-service       (s3_cold disk)    (azure_cold / gcp_cold)
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
                  └─ sends email (if SMTP_HOST set)
```

### SLO Burn Rate Engine

```
[background goroutine — every 60 seconds]
      │
      ▼
SLOWorker.tick()
  └─ ListDefinitions() from PostgreSQL
        │
        ▼  for each SLO definition:
  ComputeSLI() ← native ClickHouse query
    SELECT count(), countIf(level IN ('ERROR','FATAL'))
    FROM logs WHERE service_name = ? AND timestamp BETWEEN ? AND ?
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

### Logs

| Method | Path                | Description                   |
|--------|---------------------|-------------------------------|
| POST   | `/api/v1/logs`      | Ingest a structured log event |
| GET    | `/api/v1/logs`      | List logs (filterable)        |
| GET    | `/api/v1/logs/{id}` | Get a single log by ID        |

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

**GET query params:** `service`, `level`, `trace_id`, `from` (RFC3339), `to` (RFC3339), `page`, `page_size`

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

All SLI values are computed directly from the live ClickHouse `logs` table.

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

| Env var               | Default            | Description                                                  |
|-----------------------|--------------------|--------------------------------------------------------------|
| `ANTHROPIC_API_KEY`   | *(unset)*          | If unset, uses the rule-based `NoopAnalyzer`.                |
| `CAUSAL_MODEL`        | `claude-opus-4-7`  | Anthropic model identifier.                                  |
| `CAUSAL_DISABLED`     | *(unset)*          | Set to `true` to force the noop analyzer even if a key is set. |

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

## Running Locally (without Docker)

```bash
# Start dependencies (Postgres, Kafka, RabbitMQ) however you prefer, then:
export DATABASE_URL="postgres://pulsetrace:pulsetrace_secret@localhost:5432/pulsetrace?sslmode=disable"
export KAFKA_BROKERS="localhost:9092"
export RABBITMQ_URL="amqp://guest:guest@localhost:5672/"
export OTEL_EXPORTER_OTLP_ENDPOINT="localhost:4317"

# Apply migrations
psql $DATABASE_URL -f log-service/migrations/001_create_log_entries.sql
psql $DATABASE_URL -f alert-service/migrations/001_create_alerts.sql
psql $DATABASE_URL -f correlation-service/migrations/001_create_incidents.sql

# Run each service in a separate terminal
cd log-service          && go run ./cmd
cd alert-service        && go run ./cmd
cd correlation-service  && go run ./cmd
cd notification-service && go run ./cmd
cd gateway-service      && LOG_SERVICE_URL=http://localhost:8081 \
                           ALERT_SERVICE_URL=http://localhost:8082 \
                           CORRELATION_SERVICE_URL=http://localhost:8083 \
                           go run ./cmd
```

---

## Kubernetes Deployment

Manifests are in `k8s/`. Requires a running cluster with an nginx ingress controller.

```bash
# Apply in order
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/configmap.yaml
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

```
pulsetrace/
├── gateway-service/           # Reverse proxy + OTel trace propagation
│   ├── cmd/main.go
│   ├── internal/proxy/
│   └── Dockerfile
├── log-service/               # Log ingestion, query, Kafka publish
│   ├── cmd/main.go
│   ├── internal/handler/
│   ├── internal/repository/
│   ├── migrations/
│   └── Dockerfile
├── alert-service/             # Kafka consumer → alerts → re-publish
│   ├── cmd/main.go
│   ├── internal/consumer/
│   ├── internal/handler/
│   ├── internal/repository/
│   ├── migrations/
│   └── Dockerfile
├── correlation-service/       # Incident grouping + root-cause engine
│   ├── cmd/main.go
│   ├── internal/engine/       # Correlator (sliding window, root cause)
│   ├── internal/handler/      # Incident + timeline HTTP API
│   ├── internal/repository/
│   ├── migrations/
│   └── Dockerfile
├── notification-service/      # RabbitMQ consumer → Slack / email / log
│   ├── cmd/main.go
│   ├── internal/worker/
│   └── Dockerfile
├── shared/                    # Shared packages
│   ├── db/                    # PostgreSQL pool + ClickHouse connection
│   ├── kafka/                 # Producer + ConsumerGroup (OTel-aware)
│   ├── rabbitmq/              # Publisher + Consumer with DLQ
│   ├── middleware/            # CORS, RequestLogger, Tracing
│   ├── models/                # LogEntry, Alert, Incident, SLO, Notification
│   └── telemetry/             # OTel tracer init, Kafka header propagation
├── clickhouse/                # ClickHouse server config
│   └── storage.xml            # Tiered storage policy (S3 / Azure / GCS via env vars)
├── otel-collector/            # OTel Collector config
├── prometheus/                # Prometheus scrape config
├── grafana/                   # Pre-provisioned datasources + dashboard
├── k8s/                       # Kubernetes manifests
│   ├── namespace.yaml
│   ├── configmap.yaml
│   ├── secret.yaml
│   ├── log-service.yaml       # Deployment + Service + HPA
│   ├── alert-service.yaml
│   ├── correlation-service.yaml
│   ├── notification-service.yaml
│   └── gateway.yaml           # Deployment + Service + Ingress + HPA
├── docker-compose.yml
├── go.work
└── README.md
```

---

## Tech Stack

| Area                  | Technology                                                |
|-----------------------|-----------------------------------------------------------|
| Language              | Go 1.24                                                   |
| API                   | `net/http` (stdlib)                                       |
| Telemetry store       | ClickHouse (MergeTree, column-oriented, hot SSD tier)     |
| Cold archival         | AWS S3 / Azure Blob / GCP GCS (via ClickHouse tiered policy) |
| Local emulators       | MinIO (S3), Azurite (Azure Blob)                          |
| Relational DB         | PostgreSQL 16 (alerts, incidents, SLO metadata)           |
| Message broker        | Apache Kafka (Sarama)                                     |
| Notification queue    | RabbitMQ 3.13 (amqp091-go) with DLQ                      |
| Distributed tracing   | OpenTelemetry SDK + Jaeger                                |
| Metrics               | Prometheus + Grafana                                      |
| Causal AI             | LangChain Go (Anthropic Claude / OpenAI / Gemini / Ollama)|
| Containers            | Docker + Compose                                          |
| Orchestration         | Kubernetes (Deployments, HPA, Ingress)                    |

---

## Roadmap

- [x] **Week 1 / Phase 1** — Log ingestion, PostgreSQL, REST APIs
- [x] **Week 1 / Phase 2** — Kafka event pipeline, alert service (event-driven)
- [x] **Week 1 / Phase 3** — Distributed tracing, OpenTelemetry, Jaeger, Prometheus, Grafana
- [x] **Week 1 / Phase 4** — Incident correlation engine, RabbitMQ notifications, Kubernetes manifests
- [x] **Week 1 / Phase 5** — Causal AI: deterministic causal chains + LLM-powered narrative & confidence (LangChain Go — Claude, OpenAI, Gemini, Ollama)
- [x] **Week 2 / Phase 6** — ClickHouse analytical storage, Go Kafka batch consumer (≤10k logs / 100ms), multi-cloud cold storage tiering (AWS S3, Azure Blob, GCP GCS), native OTLP gRPC/HTTP proxying, SLO burn rate engine (real-time ClickHouse SLI queries, error budget tracking, multi-threshold alerts)
- [ ] **Week 3** — Pluggable AI adapters, dynamic log detail leveling, burn rate alerting
- [ ] **Week 4** — Zero-egress hybrid architecture, ClickHouse cluster sharding, PII sanitizer pipeline
