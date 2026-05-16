# PulseTrace — Distributed Observability & Incident Monitoring Platform

A lightweight observability platform for microservices, built entirely in Go.

> Think mini-Datadog / mini-Grafana — built by you.

---

## Architecture

### Phase 2 (current)

```
┌─────────────────────┐
│    API Gateway :8080 │   ← single entry point for all clients
└──────┬──────────────┘
       │
       ├──────────────────────────────────────┐
       ▼                                      ▼
┌─────────────────────┐            ┌─────────────────────┐
│  Log Service  :8081  │           │  Alert Service :8082 │
└──────────┬──────────┘            └──────────┬──────────┘
           │                                  │
           │  POST to Kafka "logs" topic       │  Consumes "logs" topic
           ▼                                  │  Creates alert on ERROR/FATAL
┌─────────────────────┐                       │
│       Kafka          │◄──────────────────────┘
└─────────────────────┘
           │
           ▼
┌─────────────────────┐
│     PostgreSQL       │   ← logs + alerts stored here
└─────────────────────┘
```

**Phase 3** will add OpenTelemetry distributed tracing.  
**Phase 4** will add Kubernetes manifests, Prometheus, and Grafana.

---

## What's Built

| Component        | Responsibility                                          |
|------------------|---------------------------------------------------------|
| `gateway-service`| Reverse proxy, routes `/logs` and `/alerts` to services |
| `log-service`    | Ingest & query structured log events, publish to Kafka  |
| `alert-service`  | Consume Kafka log events, create alerts for ERROR/FATAL |
| `shared`         | Common models, DB pool, Kafka producer/consumer, middleware |
| PostgreSQL       | Persistent storage for log entries and alerts           |
| Kafka            | Event bus between log-service and alert-service         |

---

## Quick Start (Docker Compose)

```bash
# 1. Clone and enter the project
git clone <repo-url> pulsetrace && cd pulsetrace

# 2. Start everything (Zookeeper, Kafka, Postgres, all services)
docker compose up --build

# 3. Ingest a log (INFO — no alert triggered)
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{
    "service": "payment-service",
    "level": "INFO",
    "message": "payment processed",
    "timestamp": "2026-05-14T10:00:00Z"
  }'

# 4. Ingest an ERROR log (alert will be created automatically)
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{
    "service": "payment-service",
    "level": "ERROR",
    "message": "database timeout",
    "trace_id": "abc-123",
    "timestamp": "2026-05-14T10:01:00Z"
  }'

# 5. Query logs
curl "http://localhost:8080/api/v1/logs?service=payment-service&level=ERROR"

# 6. Query alerts (auto-generated from ERROR/FATAL logs)
curl "http://localhost:8080/api/v1/alerts?service=payment-service"

# 7. Get a single alert by ID
curl http://localhost:8080/api/v1/alerts/<id>
```

---

## Running Locally (without Docker)

```bash
# Start PostgreSQL and Kafka however you prefer, then:
export DATABASE_URL="postgres://pulsetrace:pulsetrace_secret@localhost:5432/pulsetrace?sslmode=disable"
export KAFKA_BROKERS="localhost:9092"

# Apply migrations
psql $DATABASE_URL -f log-service/migrations/001_create_log_entries.sql
psql $DATABASE_URL -f alert-service/migrations/001_create_alerts.sql

# Run log-service
cd log-service && go run ./cmd

# In another terminal, run alert-service
cd alert-service && go run ./cmd

# In another terminal, run gateway
cd gateway-service && \
  LOG_SERVICE_URL=http://localhost:8081 \
  ALERT_SERVICE_URL=http://localhost:8082 \
  go run ./cmd
```

---

## API Reference

### Log Service (proxied via Gateway)

| Method | Path                  | Description                   |
|--------|-----------------------|-------------------------------|
| POST   | `/api/v1/logs`        | Ingest a structured log event |
| GET    | `/api/v1/logs`        | List logs (filterable)        |
| GET    | `/api/v1/logs/{id}`   | Get a single log by ID        |
| GET    | `/healthz`            | Health check                  |

#### Query Parameters for `GET /api/v1/logs`

| Param       | Type   | Description                              |
|-------------|--------|------------------------------------------|
| `service`   | string | Filter by service name                   |
| `level`     | string | DEBUG / INFO / WARNING / ERROR / FATAL   |
| `trace_id`  | string | Filter by trace ID                       |
| `from`      | RFC3339| Start of time range                      |
| `to`        | RFC3339| End of time range                        |
| `page`      | int    | Page number (default: 1)                 |
| `page_size` | int    | Results per page (default: 50, max: 200) |

---

### Alert Service (proxied via Gateway)

| Method | Path                    | Description                   |
|--------|-------------------------|-------------------------------|
| GET    | `/api/v1/alerts`        | List alerts (filterable)      |
| GET    | `/api/v1/alerts/{id}`   | Get a single alert by ID      |
| GET    | `/healthz`              | Health check                  |

#### Query Parameters for `GET /api/v1/alerts`

| Param       | Type   | Description                              |
|-------------|--------|------------------------------------------|
| `service`   | string | Filter by service name                   |
| `level`     | string | ERROR / FATAL                            |
| `from`      | RFC3339| Start of time range                      |
| `to`        | RFC3339| End of time range                        |
| `page`      | int    | Page number (default: 1)                 |
| `page_size` | int    | Results per page (default: 50, max: 200) |

---

## Event Flow (Phase 2)

```
POST /api/v1/logs
      │
      ▼
log-service: validate → persist to PostgreSQL → publish to Kafka "logs" topic
                                                          │
                                                          ▼
                                              alert-service consumer group
                                                          │
                                              level == ERROR or FATAL?
                                                    YES │
                                                        ▼
                                              insert into alerts table
```

The Kafka publish is fire-and-forget from the HTTP path — a Kafka outage never
blocks log ingestion. The alert-service retries on consumer errors and uses
consumer group offsets so no events are lost on restart.

---

## Project Structure

```
pulsetrace/
├── gateway-service/          # API Gateway (reverse proxy)
│   ├── cmd/main.go
│   ├── internal/proxy/
│   └── Dockerfile
├── log-service/              # Log ingestion & query service
│   ├── cmd/main.go
│   ├── internal/
│   │   ├── handler/          # HTTP handlers (publishes to Kafka)
│   │   └── repository/       # PostgreSQL queries
│   ├── migrations/
│   └── Dockerfile
├── alert-service/            # Kafka consumer + alert HTTP API
│   ├── cmd/main.go
│   ├── internal/
│   │   ├── consumer/         # Kafka message handler
│   │   ├── handler/          # HTTP handlers
│   │   └── repository/       # PostgreSQL queries
│   ├── migrations/
│   └── Dockerfile
├── shared/                   # Shared models, DB, Kafka, middleware
│   ├── db/
│   ├── kafka/                # Producer + ConsumerGroup wrappers
│   ├── middleware/
│   └── models/
├── docker-compose.yml
├── go.work
└── README.md
```

---

## Roadmap

- [x] **Phase 1** — Log collection, PostgreSQL, REST APIs
- [x] **Phase 2** — Kafka event pipeline, Alert Service (event-driven)
- [ ] **Phase 3** — Distributed Tracing + OpenTelemetry
- [ ] **Phase 4** — Kubernetes deployment, Prometheus, Grafana dashboards

---

## Tech Stack

| Area           | Technology              |
|----------------|-------------------------|
| Language       | Go 1.22                 |
| API            | `net/http` (stdlib)     |
| Database       | PostgreSQL 16           |
| Message Broker | Apache Kafka (via Sarama) |
| Containers     | Docker / Compose        |
| (Phase 3+)     | OpenTelemetry           |
| (Phase 4+)     | Kubernetes, Prometheus, Grafana |
