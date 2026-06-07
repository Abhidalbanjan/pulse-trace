# 🚀 Week 1 Playbook: LinkedIn Launch & Demo Recording Guide

This document is your single source of truth for preparing, recording, and publishing your first **PulseTrace** showcase post on LinkedIn tomorrow (Saturday).

---

## 1. Checklist: Pre-Requisites for Publishing

Before you publish or record, make sure you have prepared the following elements:

1. **GitHub Repository:** Ensure your repository is public and has a clean, updated `README.md` (which is already extremely detailed).
2. **Environment File (`.env`):** In your project root, make sure you have a `.env` file containing your Anthropic API Key (or OpenAI/Gemini/Ollama configurations if using another provider via LangChain Go):
   ```env
   LLM_PROVIDER=anthropic
   LLM_MODEL=claude-3-5-sonnet-20241022
   ANTHROPIC_API_KEY=your_actual_anthropic_api_key
   ```
3. **Screen Recorder:** Use a tool like OBS Studio, Loom, or macOS native screen capture (`Cmd + Shift + 5`) to capture system audio and high-resolution video.
4. **Recording Layout:** 
   - Left side: Terminal (clean window, text scaled up for readability).
   - Right side: Web browser with tabs for:
     * **Jaeger UI:** `http://localhost:16686`
     * **Incidents Endpoint:** `http://localhost:8080/api/v1/incidents`
     * **Slack/Discord Webhook Channel** (if configured to show real-time notifications).

---

## 2. Step-by-Step Demo Recording Script

Follow these steps chronologically to record your **3-minute video**.

### Step 1: Spin Up the Infrastructure
Before starting the recording, clean your environment and launch the Docker stack:
```bash
# 1. Stop any old containers and clear volumes
docker compose down -v

# 2. Build and start the microservices stack
docker compose up --build
```
*Wait 15-20 seconds to ensure Kafka, PostgreSQL, RabbitMQ, and all Go services report a "healthy" status.*

### Step 2: Begin Recording (The Hook)
* **Visual:** Code editor displaying `correlation-service/cmd/main.go` and the project file tree.
* **Action:** Tab over to your browser showing Jaeger (empty state).
* **Voiceover:** 
  > *"Hey everyone! Staring at complex Grafana dashboards trying to guess why a system broke is exhausting. To learn about backend architectures, Kafka, and OpenTelemetry in Go, I built **PulseTrace**—a distributed monitoring and correlation engine designed to automate root-cause analysis."*

### Step 3: Trigger the Cascading Outage
* **Visual:** Shift focus to your terminal (left side of the screen) and execute the automated script:
```bash
./demo_incident.sh
```
* **Visual:** Immediately focus on your browser tabs showing **Slack/Discord** or your console logs as the alerts fly in.
* **Voiceover:**
  > *"Let's simulate a cascading outage. We'll run this simulation script which injects three events: first, Postgres exhausts its connection pool; one second later, our payment-service times out on database queries; and finally, our order-service fails because the payment-service is degraded. As the script runs, our Kafka pipelines fire, RabbitMQ dispatches the notifications, and we immediately receive our alerts in real-time."*

### Step 4: Show the Incident Correlation & AI Narrative
* **Visual:** Refresh your incident endpoint in the browser: `http://localhost:8080/api/v1/incidents` (or query it via `curl` in the terminal).
* **Action:** Highlight the `causal` JSON block in the response payload.
```bash
# To fetch incidents directly from the command line:
curl -s http://localhost:8080/api/v1/incidents | json_pp
```
* **Voiceover:**
  > *"But instead of getting bombarded with three separate noisy alerts, let's query the PulseTrace Incidents API. Look at this payload. PulseTrace grouped all three alerts into a single incident. It traversed our system dependency graph deterministically to build this causal chain. Then, it fed this evidence to Claude via LangChain Go, producing a plain-English narrative of what failed, why, and a concrete checklist of SRE action items to resolve the database bottleneck."*

### Step 5: Close the Video
* **Visual:** Show the `README.md` or `AI_OBSERVABILITY_PLAN.md` file in your editor.
* **Voiceover:**
  > *"This project has been an incredible sandbox for learning message brokers and telemetry. Next, I'm taking this a step further: replacing Postgres tables with a live topology graph in Neo4j to build predictive failure warnings. Check out the GitHub link below to review the code or collaborate. Thanks for watching!"*

---

## 3. How Failures Are Correlated Under the Hood

To explain the project with authority in your post comments and video, here is exactly how PulseTrace correlates these cascade failures:

```
[postgres: Pool Exhausted] ──(1s)──> [payment-service: Timeout] ──(1s)──> [order-service: Unavailable]
          │                                      │                                    │
          ▼                                      ▼                                    ▼
 Kafka: 'logs' (ERROR)                  Kafka: 'logs' (ERROR)               Kafka: 'logs' (ERROR)
          │                                      │                                    │
    (alert-service)                        (alert-service)                      (alert-service)
          ▼                                      ▼                                    ▼
 Kafka: 'alerts' (ERROR)                Kafka: 'alerts' (ERROR)              Kafka: 'alerts' (ERROR)
          │                                      │                                    │
          └───────────────────────────────┬───────────────────────────────────────────┘
                                          ▼
                             (correlation-service consumer)
                                          │
                        5-Min sliding window + Dependency Lookup
                                          │
                                          ▼
                         Groups into a Single OPEN Incident!
```

### A. Context Propagation (OTel & Kafka)
* When Gateway proxies the log-ingestion requests, it injects W3C `traceparent` headers.
* The Log Service extracts this context and propagates it when publishing to the Kafka `logs` topic using **Kafka record headers**.
* The Alert and Correlation services extract these headers, starting child spans that map a cohesive trace inside Jaeger.

### B. Sliding Window & Dependency Clustering
* The Correlation Service consumes the Kafka `alerts` topic.
* When an alert arrives (e.g., `payment-service` timeout), the service queries Postgres to see if there is an active `OPEN` incident within a **5-minute sliding window** (`started_at >= alert.triggered_at - 5 minutes`) that belongs to the service itself OR any of its declared dependencies:
  ```go
  // Inside internal/repository/incident_repository.go: GetOpenByWindow()
  // payment-service depends on postgres, auth-service, and kafka.
  // order-service depends on payment-service, postgres, and kafka.
  ```
* Because `postgres` failed first, a new incident was created. When `payment-service` fails 1 second later, the correlation engine matches it to the existing `postgres` incident because `postgres` is in `payment-service`'s candidate dependencies!
* This groups them into a single incident, increments the `alert_count`, and promotes the severity.

### C. Causal AI Analysis (LangChain Go)
* In the background, the Correlation Service calls the Causal Analyzer:
  1. It sorts the incident's alerts by timestamp ascending.
  2. It traverses the dependency graph: for each alert, it checks if there is an upstream service that triggered an alert earlier. It builds the **Causal Chain** (e.g., `postgres` -> `payment-service` -> `order-service`).
  3. It bundles this chain, the alert messages, and the system dependencies into a prompt.
  4. It sends this to Claude (or another LLM via our newly integrated `LangChainAnalyzer`).
  5. The model refines the root cause, generates a high-fidelity narrative, and provides SRE checklist actions, returning a structured JSON payload which is persisted to PostgreSQL and sent to notifications.
