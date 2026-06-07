# PulseTrace: Final Product System Architecture

This document outlines the final production-ready system architecture of PulseTrace, illustrating telemetry ingestion pipelines, the zero-data-egress compliance boundaries, hybrid multi-cloud storage tiering, causal AI diagnostics, and self-healing playbooks.

---

## 1. High-Level Data & Control Plane

The diagram below displays the end-to-end flow of telemetry data, feedback loops for dynamic logging, and the partition between the local client environment (Zero-Data-Egress boundary) and the control plane.

```mermaid
flowchart TB
    subgraph "Client App Environment (K8s)"
        APP[App Microservices] -->|OTel Traces & Metrics| AGENT[PulseTrace Agent]
        AGENT -->|Dynamic Config Listener| AGENT
    end

    subgraph "PulseTrace On-Premises Data Plane (Zero-Data-Egress Boundary)"
        GW[API Gateway / Auth] -->|Logs & Metrics| KAFKA_LOGS[(Kafka: logs topic)]
        GW -->|OTLP Traces| OTel_COL[OpenTelemetry Collector]
        
        KAFKA_LOGS -->|Batch Ingestion| CH_WRITER[ClickHouse Batch Writer]
        CH_WRITER -->|Hot Logs & Metrics| CH[(ClickHouse SSD Cluster)]
        
        %% Tiered Storage Policy
        CH -->|Compacted Cold Logs| STORAGE_TIER{Storage Tier Manager}
        STORAGE_TIER -->|S3 API| AWS_S3[(AWS S3)]
        STORAGE_TIER -->|Blob API| AZURE_BLOB[(Azure Blob)]
        STORAGE_TIER -->|GCS API| GOOGLE_GCS[(GCS / MinIO / Ceph)]
        
        OTel_COL -->|Span Analysis| TOPO_BUILDER[Topology Graph Builder]
        TOPO_BUILDER -->|Cypher Queries| NEO4J[(Neo4j Graph Database)]
    end

    subgraph "Core Observability Alerting & AI Control Plane"
        KAFKA_LOGS -->|Alert Filtering Engine| ALERT_SERV[Alert Service]
        ALERT_SERV -->|Kafka: alerts topic| KAFKA_ALERTS[(Kafka: alerts)]
        
        KAFKA_ALERTS -->|Correlation Engine| CORR_SERV[Correlation Service]
        NEO4J <-->|Context Queries| CORR_SERV
        
        CORR_SERV -->|Burn Rate Check| SLO_WORKER[SLO SLO-Worker]
        CORR_SERV -->|Causal Chain| AI_ROUTER{AI Provider Router}
        
        AI_ROUTER -->|On-Premise LLM| LOCAL_LLM[LangChain / Local Ollama]
        AI_ROUTER -->|Cloud Secure APIs| CLOUD_LLM[GPT-4 / Claude / Gemini]
        
        CORR_SERV -->|1. Trigger Dynamic Logging| GW
        GW -->|2. Toggle Agent Debug Verbosity| AGENT
        
        CORR_SERV -->|3. Trigger Remediation| RUNBOOK_ENG[Self-Healing Runbook Executor]
        RUNBOOK_ENG -->|4. Deploy Fix / Restart Pod| APP
    end

    subgraph "Notification & User Interface"
        CORR_SERV -->|Incident Message| RMQ[(RabbitMQ)]
        RMQ -->|Notification Dispatcher| NOTIFY[Notification Service]
        NOTIFY -->|Integrations| SLACK[Slack / PagerDuty / Webhooks]
        
        UI[SaaS Dashboard UI] <-->|Secure Metadata APIs| GW
    end
```

---

## 2. Core Architectural Subsystems

### A. Telemetry Storage Pipeline (ClickHouse Multi-Cloud Storage Tiering)
To achieve Datadog-grade querying at a fraction of the cost, PulseTrace implements a hybrid multi-cloud cold storage strategy:
1.  **Ingestion:** The Batch Writer collects Go struct representations of OTel payloads and commits them to ClickHouse using multi-threaded block writes (buffers of 10,000 items or 100ms timeouts).
2.  **Hot Tier:** The last 7 days of logs, traces, and metrics are written directly to fast local storage (NVMe/SSD) for active incident investigation.
3.  **Cold Tier:** ClickHouse's tiered storage manager automatically compresses data (typically achieving 5x–10x compression ratios) and pushes it to object stores (Amazon S3, Azure Blob, Google GCS, or private MinIO instances). Active queries seamlessly traverse both storage tiers without manual intervention.

---

### B. Dynamic Logging & Anomaly Feedback Loops
Instead of constantly paying for the ingestion of verbose trace data and debug logs:
1.  Agents run in **Minimal Mode**, collecting high-level system metrics and error logs.
2.  The **SLO Burn Rate Worker** and **Anomaly Detector** continuously evaluate system thresholds.
3.  Upon detecting an anomaly, a socket backchannel command is sent through the Gateway to the local agents on the affected microservices.
4.  The agents dynamically elevate the logging level to `DEBUG` and set tracing sampling to `100%` for specific API routes. Once the system health returns to normal, the system drops back to Minimal Mode.

---

### C. Root Cause Analysis (RCA) & AI Remediation
1.  **Topology Discovery:** The Topology Service automatically builds service maps in Neo4j by tracing parent-child calls from OpenTelemetry spans.
2.  **Causal Analysis:** When alerts fire, the Correlation Service queries Neo4j to build a failure propagation path (e.g., *Frontend* $\rightarrow$ *Orders-Service* $\rightarrow$ *Database*).
3.  **Narrative & Self-Healing:** The pluggable AI router sends this causal chain to the configured LLM. The model creates a natural-language description and identifies the remedy. The Runbook Executor runs automated restoration actions (like cycling connections or restarting crashed pods) to minimize MTTR (Mean Time to Resolution).
