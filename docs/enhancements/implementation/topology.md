# Topology — Implementation Plan

Spec: [../topology.md](../topology.md) · Service: **topology-service** (Neo4j) + **gateway** proxy · View: `frontend/src/components/Topology/TopologyView.tsx`

## Current state (grounded)
- ReactFlow+dagre graph with search/focus/health filter, blast-radius, health legend (F13). `/api/v1/topology/{graph,dependencies/*}`; causal paths pushed to Neo4j.

## E1 — Live traffic & latency on edges · M  *(recommended first slice)*
- `GET /api/v1/topology/edges?range=` → parent→child service edge metrics (rate/error/p95) from `otel_traces` (gateway, `queryScoped`) — shares logic with Traces E4 (`buildTraceServiceMapSQL`). FE weighted/colored edges + live-refresh toggle. Parity: route consumed.
- Tests: edge-rollup SQL/aggregation; e2e edges carry weights.

## E2 — Anomaly & incident overlay · M
- Join F14 anomaly state + incident causal path (Neo4j) into the graph payload; FE pulsing anomaly nodes + highlighted causal edges.

## E5 — Node drill-in · S
- Node side-panel = service RED signals (reuse Services E1) + deep links.

## E4 — Grouping / clustering · M
- Collapsible groups by namespace/team/tier (from Catalog labels) with rolled-up health; FE group nodes.

## E3 — Time-travel · M
- Time-parameterized graph + edge metrics + health as of a timestamp; FE time scrubber linked from incidents.

## E6 — Saved views & export · S
- `topology_views` per tenant (filters+layout); FE view switcher + PNG/JSON export.

## Sequencing & gates
E1 → E2 → E5 → E4 → E3 → E6. Per slice: touched module build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(topology): …`.
