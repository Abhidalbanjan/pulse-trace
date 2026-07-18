# PulseTrace Frontend UI/UX Architecture Spec

This document outlines the complete page structure, feature set, layout components, and Role-Based Access Control (RBAC) definitions for the PulseTrace Next.js frontend. This serves as the blueprint for Figma designs and frontend development.

## 1. Global Application Shell
All pages (except onboarding) are wrapped in the Global Shell.
*   **Global Sidebar (Left):** Icons for Navigation (Dashboard, Explorer, Topology, Incidents, Service Catalog, Settings).
*   **Global Header (Top):** 
    *   **Environment Switcher:** Dropdown (`Production`, `Staging`, `All`).
    *   **Global Time Picker:** (`Last 15m`, `Last 1hr`, `Custom`).
    *   **Omni-Search Bar:** Quickly search for a specific trace ID, service name, or incident.
    *   **User Avatar & Profile:** Manage personal settings.

---

## 2. Page Directory & Feature Mapping

### 2.1. Frictionless Onboarding Wizard (Route: `/onboarding`)
*   **Purpose:** The first screen a new tenant sees. Gets them ingesting data in < 5 mins.
*   **Sections:**
    *   **API Key Generator:** Securely displays the initial ingestion API key.
    *   **Platform Selector:** Tabs for `Kubernetes`, `Docker`, `Linux`, `Datadog Migration`.
    *   **The "Trojan Horse" Snippet:** Provides the exact 1-line `curl` script or Datadog agent URL override to start sending data.
    *   **Live Connection Listener:** A pulsing radar animation that turns Green the millisecond PulseTrace receives the first log from their agent.

### 2.2. Executive Cost & Health Dashboard (Route: `/dashboard`)
*   **Purpose:** The home page. Proves the ROI of PulseTrace immediately.
*   **Sections:**
    *   **Cost Savings Widget:** "90% saved vs Datadog". Shows GBs routed to cheap S3 vs Hot Storage.
    *   **Cluster Health Heatmap:** A visual matrix of all services. Green = Healthy, Red = Alerting.
    *   **AI Log Leveling Status:** Shows how much `DEBUG` log volume the AI has actively dropped today to save money.

### 2.3. Telemetry Explorer (Route: `/explorer`)
*   **Purpose:** Deep-dive investigation for Logs and Traces. High-density, Datadog-style.
*   **Sections:**
    *   **Faceted Sidebar:** Filter by `service`, `level` (ERROR, WARN), `env`, and `team`.
    *   **Log Histogram Chart:** A bar chart showing log volume over time.
    *   **Outlier Analysis (BubbleUp):** A mode where dragging over a latency spike instantly compares those logs to the baseline, highlighting the specific tag (e.g., `user_id`) causing the anomaly.
    *   **High-Density Datatable:** Virtualized list displaying timestamp, service, log level, and the log message.
    *   **Slide-Out Drawer (Context):** Clicking a log row slides open a drawer from the right side. It shows the full JSON payload, host metrics, and a button to "View Trace".

### 2.4. Trace Flame Graph Viewer (Route: `/explorer/trace/[id]`)
*   **Purpose:** Waterfall visualization of a single distributed trace.
*   **Sections:**
    *   **Span Gantt Chart:** Visualizes exactly how many milliseconds every function/service took in the request.
    *   **Critical Path Highlight:** AI automatically highlights the specific span (e.g., a slow DB query) that caused the latency.

### 2.5. Interactive Topology Map (Route: `/topology`)
*   **Purpose:** Architectural bird's-eye view.
*   **Sections:**
    *   **React Flow Node Graph:** Microservices connected by lines.
    *   **Real-time Edge Traffic:** Lines animate to show traffic volume and latency.
    *   **Incident HUD (Right Panel):** If a node is Red, the HUD shows active alerts for that node.

### 2.6. Incident Command Center (Route: `/incidents`)
*   **Purpose:** Where SREs go when pagerduty goes off.
*   **Sections:**
    *   **Active Incidents Table:** List of grouped alerts with severity scoring.
    *   **AI Root Cause Modal:** Clicking an incident opens a modal containing the LangChain-generated natural language summary ("Memory leak detected in Payment pod due to missing limits").
    *   **Self-Healing Playbooks:** "1-Click" buttons suggested by AI to resolve the issue (e.g., "Restart Pod", "Scale Up Group").

### 2.7. Service Catalog (Route: `/catalog`)
*   **Purpose:** Engineering inventory and ownership.
*   **Sections:**
    *   **Service List:** Table of all discovered services.
    *   **Ownership Metadata:** Shows which Team owns the service, link to GitHub Repo, link to Slack Channel, and current On-Call engineer.

---

## 3. RBAC & Security Specification (Route: `/settings/roles`)

PulseTrace will use **Attribute-Based Access Control (ABAC)** combined with standard Roles to provide Enterprise-grade security.

### 3.1. Standard Roles
*   **Platform Admin:** Full access. Can modify SSO, billing, and global alert thresholds.
*   **SRE / Editor:** Can view all telemetry, acknowledge incidents, and execute Self-Healing Runbooks.
*   **Developer / Viewer:** Read-only access to Logs, Traces, and Topology. Cannot execute runbooks.

### 3.2. Tag-Based Data Isolation (The Enterprise Feature)
Unlike cheap tools, PulseTrace allows isolating data based on tags (e.g., `team:payments` or `env:production`).
*   **How it looks in UI:** In the Role Creation screen, an Admin can assign a "Data Boundary" to a user group. 
*   **Example:** If a junior developer on the Frontend team logs in, their Data Boundary is set to `team:frontend`. The **Global Application Shell** silently applies this filter to every page. They will *never* see logs or traces from the `team:payment` database, ensuring PCI/PII compliance across large organizations.
