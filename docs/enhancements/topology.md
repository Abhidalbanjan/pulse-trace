# Topology — Enhancement Spec

**Route:** `/topology` · **Component:** `frontend/src/components/Topology/TopologyView.tsx` · **Backend:** topology-service (Neo4j), gateway `/api/v1/topology/{graph,dependencies/*}`, `/api/v1/incidents`

## 1. Where it stands

- An interactive dependency graph (ReactFlow + dagre) with **search / focus / health filter**, blast-radius highlighting, and a health legend (F13). Causal paths are pushed to Neo4j.

## 2. Market-ready gap

It's a good static map. The market bar (Datadog Service Map, Kiali, Instana) is a **living** map: real-time **traffic/latency on edges**, **anomaly overlay**, **time-travel** (what did the map look like during the incident), and **grouping** by namespace/team/tier so large graphs stay legible.

## 3. Proposed enhancements

### E1. Live traffic & latency on edges · **M**
- **User value:** the map breathes — edge thickness = request rate, color = error/latency; see load shift in real time.
- **What:** annotate edges with RED metrics from traces; animate/refresh.
- **Backend:** trace-derived edge metrics (`GET /api/v1/topology/edges?range=`) — shares data with Traces' service map.
- **Frontend:** weighted/colored edges + a live refresh toggle.

### E2. Anomaly & incident overlay · **M**
- **User value:** unhealthy nodes and the causal chain of an active incident light up on the map.
- **What:** overlay anomaly state (F14) + the incident causal path onto nodes/edges.
- **Backend:** join anomaly + incident causal path (already in Neo4j) into the graph payload.
- **Frontend:** pulsing anomaly nodes + highlighted causal edges.

### E3. Time-travel · **M**
- **User value:** *"show the topology + health at the moment INC-142 started."*
- **What:** a time selector that renders the graph + edge metrics + health as of a timestamp.
- **Backend:** time-parameterized graph + edge metrics.
- **Frontend:** time scrubber linked from incidents.

### E4. Grouping / clustering · **M**
- **User value:** 200 services collapse into namespaces/teams/tiers you can expand — legible at scale.
- **What:** collapsible groups by namespace/team/tier with rolled-up health.
- **Backend:** group metadata from Catalog/labels.
- **Frontend:** group nodes with expand/collapse.

### E5. Click-through to golden signals · **S**
- **User value:** click a node → its RED signals + SLO + incidents, without leaving context.
- **What:** a node side-panel summarizing the service (reuse Services signals).
- **Frontend:** node drawer with signals + deep links.

### E6. Saved views & export · **S**
- **User value:** save a focused view (one team's subgraph); export/share a snapshot.
- **What:** persist named views (filters + layout); export PNG/JSON.
- **Backend:** `topology_views` per tenant.
- **Frontend:** view switcher + export.

## 4. Market-ready DoD

- Edges show live traffic/latency; anomalies and active-incident causal paths overlay the map.
- Time-travel reconstructs the map during an incident; large graphs group by namespace/team; nodes drill into golden signals.

## 5. Suggested sequence

E1 (live edges) → E2 (anomaly/incident overlay) → E5 (node drill-in) → E4 (grouping) → E3 (time-travel) → E6 (saved views).
