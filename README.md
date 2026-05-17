# PulseTrace — Distributed Observability & Incident Monitoring Platform

A production-grade observability platform for microservices, built entirely in Go.

> Think mini-Datadog / mini-Grafana — built from scratch.

---

## Architecture

```
                        ┌──────────────────────────────┐
                        │     API Gateway  :8080        │
                        │  (reverse proxy + tracing)    │
                        └──┬──────────┬──────────┬──────┘
                           │          │          │
                    ┌──────▼──┐  ┌────▼────┐  ┌─▼──────────────┐
                    │  Log    │  │  Alert  │  │  Correlation    │
                    │ Service │  │ Service │  │    Service      │
                    │  :8081  │  │  :8082  │  │     :8083       │
                    └────┬────┘  └────┬────┘  └────────┬────────┘
                         │            │                 │
                    Kafka "logs"  Kafka "alerts"   RabbitMQ
                         │            │                 │
                         └────────────┘                 │
                                  │                     ▼
                             ┌────▼────┐     ┌──────────────────┐
                             │  Kafka  │     │  Notification    │
                             └────┬────┘     │    Service       │
                                  │          └──────────────────┘
                             ┌────▼────┐
                             │Postgres │  ← logs, alerts, incidents
                             └─────────┘

  ┌─────────────────────────────────────────────────────────────────┐
  │                    Observability Stack                          │
  │  OTel Collector → Jaeger (traces)  ·  Prometheus → Grafana     │
  └─────────────────────────────────────────────────────────────────┘
```

---

## What's Built

| Component              | Responsibility                                                        |
|------------------------|-----------------------------------------------------------------------|
| `gateway-service`      | Reverse proxy, W3C trace context propagation, routes all API traffic  |
| `log-service`          | Ingest & query structured log events, publish to Kafka `logs` topic   |
| `alert-service`        | Consume `logs` topic, create alerts for ERROR/FATAL, publish to `alerts` topic |
| `correlation-service`  | Consume `alerts` topic, group into incidents, infer root cause, publish to RabbitMQ |
| `notification-service` | Consume RabbitMQ, dispatch to Slack / email / log                     |
| `shared`               | Models, DB pool, Kafka producer/consumer, RabbitMQ client, OTel middleware |
| PostgreSQL             | Persistent storage for log entries, alerts, and incidents             |
| Kafka                  | Event bus: `logs` and `alerts` topics                                 |
| RabbitMQ               | Notification pipeline with dead-letter queue                          |
| OTel Collector         | Receives OTLP spans from all services, forwards to Jaeger             |
| Jaeger                 | Distributed trace visualization                                       |
| Prometheus             | Metrics scraping from OTel Collector                                  |
| Grafana                | Pre-provisioned dashboards for traces and metrics                     |

---

## Quick Start

```bash
# 1. Clone
git clone <repo-url> pulsetrace && cd pulsetrace

# 2. Build and start the full stack
docker compose up --build

# 3. Ingest an INFO log (no alert)
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service": "auth-service", "level": "INFO", "message": "user login successful"}'

# 4. Ingest an ERROR log (triggers alert → incident → notification)
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service": "payment-service", "level": "ERROR", "message": "DB connection pool exhausted"}'

# 5. Query logs
curl "http://localhost:8080/api/v1/logs?service=payment-service&level=ERROR"

# 6. Query alerts
curl "http://localhost:8080/api/v1/alerts"

# 7. Query incidents (grouped + root cause)
curl "http://localhost:8080/api/v1/incidents"

# 8. Get incident timeline
curl "http://localhost:8080/api/v1/incidents/<id>/timeline"
```

---

## Observability UIs

| UI              | URL                        | Credentials   |
|-----------------|----------------------------|---------------|
| Jaeger          | http://localhost:16686     | —             |
| Grafana         | http://localhost:3000      | admin / admin |
| Prometheus      | http://localhost:9090      | —             |
| RabbitMQ Mgmt   | http://localhost:15672     | pulsetrace / pulsetrace_secret |

---

## Event Flow

```
POST /api/v1/logs
      │
      ▼
gateway-service
  └─ injects W3C traceparent header
      │
      ▼
log-service
  ├─ validates & persists to PostgreSQL
  ├─ starts OTel span (child of gateway span)
  └─ publishes to Kafka "logs" topic (with trace headers)
                    │
                    ▼
            alert-service consumer
              ├─ extracts trace context from Kafka headers
              ├─ level == ERROR or FATAL?
              │     YES → insert alert into PostgreSQL
              └─ publishes to Kafka "alerts" topic (trace propagated)
                              │
                              ▼
                  correlation-service consumer
                    ├─ extracts trace context
                    ├─ finds open incident in 5-min window for service
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
│   ├── db/                    # PostgreSQL pool
│   ├── kafka/                 # Producer + ConsumerGroup (OTel-aware)
│   ├── rabbitmq/              # Publisher + Consumer with DLQ
│   ├── middleware/            # CORS, RequestLogger, Tracing
│   ├── models/                # LogEntry, Alert, Incident, Notification
│   └── telemetry/             # OTel tracer init, Kafka header propagation
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

| Area                | Technology                              |
|---------------------|-----------------------------------------|
| Language            | Go 1.22                                 |
| API                 | `net/http` (stdlib)                     |
| Database            | PostgreSQL 16                           |
| Message broker      | Apache Kafka (Sarama)                   |
| Notification queue  | RabbitMQ 3.13 (amqp091-go) with DLQ    |
| Distributed tracing | OpenTelemetry SDK + Jaeger              |
| Metrics             | Prometheus + Grafana                    |
| Containers          | Docker + Compose                        |
| Orchestration       | Kubernetes (Deployments, HPA, Ingress)  |

---

## Roadmap

- [x] **Phase 1** — Log ingestion, PostgreSQL, REST APIs
- [x] **Phase 2** — Kafka event pipeline, alert service (event-driven)
- [x] **Phase 3** — Distributed tracing, OpenTelemetry, Jaeger, Prometheus, Grafana
- [x] **Phase 4** — Incident correlation engine, RabbitMQ notifications, Kubernetes manifests
