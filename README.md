# PulseTrace — Distributed Observability & Incident Monitoring Platform

A lightweight observability platform for microservices, built entirely in Go.

> Think mini-Datadog / mini-Grafana — built by you.

---

## Architecture

```
┌─────────────────────┐
│    API Gateway :8080 │   ← single entry point for all clients
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│  Log Service  :8081  │   ← ingest & query structured logs
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│     PostgreSQL       │   ← persistent log storage
└─────────────────────┘
```

**Phase 2** will add Kafka, Metrics Service, and Tracing Service.  
**Phase 3** will add OpenTelemetry.  
**Phase 4** will add RabbitMQ alerting and Kubernetes manifests.

---

## Phase 1 — What's Built

| Component        | Responsibility                              |
|------------------|---------------------------------------------|
| `gateway-service`| Reverse proxy, routes requests to services  |
| `log-service`    | Ingest & query structured log events        |
| `shared`         | Common models, DB pool, HTTP middleware      |
| PostgreSQL       | Persistent storage for log entries          |

---

## Quick Start (Docker Compose)

```bash
# 1. Clone and enter the project
git clone <repo-url> pulsetrace && cd pulsetrace

# 2. Start everything
docker compose up --build

# 3. Ingest a log
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{
    "service": "payment-service",
    "level": "ERROR",
    "message": "database timeout",
    "trace_id": "abc-123",
    "timestamp": "2026-05-13T10:00:00Z"
  }'

# 4. Query logs
curl "http://localhost:8080/api/v1/logs?service=payment-service&level=ERROR&page=1&page_size=10"

# 5. Get a single log by ID
curl http://localhost:8080/api/v1/logs/<id>
```

---

## Running Locally (without Docker)

```bash
# Start PostgreSQL however you prefer, then:
export DATABASE_URL="postgres://pulsetrace:pulsetrace_secret@localhost:5432/pulsetrace?sslmode=disable"

# Apply migration
psql $DATABASE_URL -f log-service/migrations/001_create_log_entries.sql

# Run log-service
cd log-service && go run ./cmd

# In another terminal, run gateway
cd gateway-service && LOG_SERVICE_URL=http://localhost:8081 go run ./cmd
```

---

## API Reference

### Log Service (proxied via Gateway)

| Method | Path                  | Description                  |
|--------|-----------------------|------------------------------|
| POST   | `/api/v1/logs`        | Ingest a structured log event|
| GET    | `/api/v1/logs`        | List logs (filterable)       |
| GET    | `/api/v1/logs/{id}`   | Get a single log by ID       |
| GET    | `/healthz`            | Health check                 |

#### Query Parameters for `GET /api/v1/logs`

| Param       | Type   | Description                        |
|-------------|--------|------------------------------------|
| `service`   | string | Filter by service name             |
| `level`     | string | DEBUG / INFO / WARNING / ERROR / FATAL |
| `trace_id`  | string | Filter by trace ID                 |
| `from`      | RFC3339| Start of time range                |
| `to`        | RFC3339| End of time range                  |
| `page`      | int    | Page number (default: 1)           |
| `page_size` | int    | Results per page (default: 50, max: 200) |

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
│   │   ├── handler/
│   │   └── repository/
│   ├── migrations/
│   └── Dockerfile
├── shared/                   # Shared models, DB, middleware
│   ├── db/
│   ├── middleware/
│   └── models/
├── docker-compose.yml
├── go.work
└── README.md
```

---

## Roadmap

- [x] **Phase 1** — Log collection, PostgreSQL, REST APIs
- [ ] **Phase 2** — Kafka event pipeline, Metrics Service
- [ ] **Phase 3** — Distributed Tracing + OpenTelemetry
- [ ] **Phase 4** — RabbitMQ alerting, Kubernetes deployment, Grafana dashboards

---

## Tech Stack

| Area           | Technology          |
|----------------|---------------------|
| Language       | Go 1.22             |
| API            | `net/http` (stdlib) |
| Database       | PostgreSQL 16       |
| Containers     | Docker / Compose    |
| (Phase 2+)     | Kafka, Redis, MongoDB |
| (Phase 3+)     | OpenTelemetry       |
| (Phase 4+)     | RabbitMQ, Kubernetes, Prometheus, Grafana |
