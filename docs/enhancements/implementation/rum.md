# Real User Monitoring — Implementation Plan

Spec: [../rum.md](../rum.md) · Service: **gateway** (ClickHouse `rum_events`) · View: `frontend/src/components/RUM/RUMView.tsx`

## Current state (grounded)
- Web-vitals trends (p75), session stitching, device breakdown, RUM errors, analytics (`/api/v1/rum/{ingest,analytics,trends,sessions,devices,errors}`) (F9). `classifyUserAgent` pure helper.

## E4 — CWV by page / geo / device · M  *(recommended first slice)*
- `GET /api/v1/rum/web-vitals?dimension=page|geo|device&range=` → LCP/INP/CLS p75 + pass/fail vs Google thresholds, grouped. Pure `cwvVerdict(metric, p75)`. FE breakdown tables (+ geo map later). Parity: route consumed.
- Tests: `cwvVerdict` thresholds; e2e breakdown renders.

## E1 — Session timeline (lightweight replay) · L
- `GET /api/v1/rum/sessions/{id}` → ordered events (navigations, long tasks, XHR/fetch, errors, per-view vitals). Pure `assembleSessionTimeline(events)`. FE timeline view from the sessions table.

## E3 — RUM → backend trace linking · M
- Correlate RUM resource/XHR events to `trace_id` (propagate on ingest); FE "view backend trace" → `/traces`.

## E2 — Frustration signals · M
- Capture click/interaction events; pure `detectFrustration(events)` (rage/dead/error clicks); FE frustration panel + per-session markers.

## E5 — User-journey funnels · M
- `GET /api/v1/rum/funnel?steps=` funnel aggregation over view events; FE funnel builder.

## E6 — Real-time active users · S
- Recent-window session count; FE live tile.

## Sequencing & gates
E4 → E1 → E3 → E2 → E5 → E6. Per slice: gateway build/vet/test (ratchet), FE gates, parity, govulncheck, e2e; commit `feat(rum): …`.
