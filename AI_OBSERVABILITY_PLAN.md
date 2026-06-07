# PulseTrace — Next-Gen AI Observability & Predictive Incident Platform

A production-ready observability platform for microservices, built entirely in Go. PulseTrace represents a paradigm shift: moving **way beyond traditional dashboards** into proactive, AI-driven, and graph-based observability.

> Think mini-Datadog / mini-Grafana — but natively designed for AI root-cause analysis, predictive failure detection, and live graph visualization.

---

## 🚀 The Vision: Beyond Traditional Dashboards

Modern microservice architectures are too complex for static metric dashboards and reactive alerts. SREs need to know not just *that* something broke, but **why** it broke, **what** it will impact next, and **how** to fix it. 

PulseTrace is evolving to provide a **Single Pane of Glass** for SREs:
1. **Live System Topology Graph (Neo4j):** Visualize all services, dependencies, and state on a dynamic graph.
2. **Predictive Failure Detection:** See which services are "about to fail" directly on the graph (e.g., nodes glowing yellow/red) based on anomalies before hard alerts trigger.
3. **Causal Inference Engine:** When a failure occurs, the AI instantly evaluates the entire state to declare *what* failed, *why* it failed, and *what actions the SRE must take*.

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
                             ┌────▼─────────────▼────┐
                             │ Postgres / Neo4j DB   │  ← topology, logs, alerts, causal incidents
                             └───────────────────────┘

  ┌─────────────────────────────────────────────────────────────────┐
  │                    Observability Stack                          │
  │  OTel Collector → Neo4j UI Graph / Causal Inference Insights    │
  └─────────────────────────────────────────────────────────────────┘
```

---

## The AI Causal Inference Engine

Pattern matching tells you *what kind* of error happened. **Causal AI** answers fundamentally harder questions: *what caused what*, *what fails next*, and *how to fix it*.

When an incident or anomaly is detected, the correlation service:

1. **Builds a deterministic causal chain** by querying the Neo4j dependency graph in temporal order — finding the earliest preceding anomaly or alert from a known upstream service.
2. **Evaluates Predictive State** to identify downstream services that are currently healthy but probabilistically likely to fail (blast radius).
3. **Hands the chain + topology + evidence to Claude** (via Anthropic API) to refine the hypothesis, produce a confidence score, and generate **actionable SRE remediations**.
4. **Surfaces the result** on the single-pane Graph UI.

### Example AI Output

```json
{
  "incident_id": "e4661798-...",
  "status": "PREDICTIVE_WARNING",
  "root_cause": "Postgres connection pool exhaustion — likely runaway query or insufficient pool size.",
  "causal_chain": [
    { "from": "postgres", "to": "payment-service", "evidence": "postgres connection pool exhausted at 13:45:38" },
    { "from": "payment-service", "to": "order-service", "evidence": "predictive: downstream dependency saturation" }
  ],
  "narrative": "The incident originated in postgres with connection pool exhaustion. This is causing payment-service timeouts. **Predictive Alert:** order-service is at 90% likelihood of failing within 2 minutes due to queued payment requests.",
  "sre_actions": [
    "Run `SELECT * FROM pg_stat_activity` to identify the blocking transaction.",
    "Temporarily scale up the payment-service pod replicas to buffer incoming traffic.",
    "Increase `max_connections` in Postgres config if CPU allows."
  ],
  "confidence": 0.87
}
```

---

## Roadmap / Implementation Plan

### Phase 1: Dynamic Graph Integration (Neo4j)
- [ ] Spin up Neo4j instance via `docker-compose.yml`.
- [ ] Implement a `topology-service` or extend `correlation-service` to write service dependencies to Neo4j.
- [ ] Ingest OTel tracing data to automatically infer and update dependency edges in the graph in real-time.

### Phase 2: Predictive Failure Engine
- [ ] Expand the anomaly detection logic (currently in `correlation-service`).
- [ ] Write logic to flag a service as "Degraded" or "Predictive Warning" if metrics (e.g., latency, error rate) trend upwards before hard thresholds are crossed.
- [ ] Map these states back into the Neo4j nodes.

### Phase 3: SRE "Single Pane" UI
- [ ] Scaffold a React/Next.js frontend.
- [ ] Integrate a graph visualization library (like Cytoscape.js, React-Flow, or Neo4j Bloom).
- [ ] Bind real-time data to the graph: Nodes should dynamically change color (Green -> Yellow -> Red) based on health status.
- [ ] Implement a side-panel for the Causal AI to chat/display the narrative, prediction, and SRE actions.

### Phase 4: Enhanced Causal AI with Graph Context
- [ ] Update the Anthropic/Claude prompt to ingest the localized Neo4j subgraph surrounding an incident.
- [ ] Ask the LLM to specifically output `sre_actions` and `predictive_blast_radius` in its JSON schema.
- [ ] Surface this enhanced analysis into the UI side-panel.
