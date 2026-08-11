# Incidents — Implementation Plan

Spec: [../incidents.md](../incidents.md) · Service: **correlation-service** · View: `frontend/src/components/Incidents/IncidentsView.tsx`

## Current state (grounded)

- `IncidentRepository` (Postgres), `Correlator`, causal `Analyzer` + `GroundAnalysis` guardrail, `RemediationPanel` (policy-gated), timeline (`/incidents/{id}/timeline`), causal-provider health.
- Routes: `GET /incidents`, `GET /incidents/{id}`, `/incidents/{id}/timeline`, playbook endpoints.
- LLM: `FallbackProvider` chat provider + `causal.Evidence` bundle assembled in `scheduleCausalAnalysis`.

## E1 — AI-drafted postmortem · M  *(recommended first slice)*

- **Data:** `incident_postmortems(id, incident_id UNIQUE, tenant_id, content TEXT, model, generated_at, edited_at)` — migration **006**.
- **Backend (correlation-service):**
  - Reuse the `causal.Evidence` assembly (incident + alerts + timeline + causal analysis + linked deploys if available).
  - Pure `buildPostmortemPrompt(evidence) string` (structured sections: summary, impact, timeline, root cause, contributing factors, action items) — unit-testable (deterministic string from inputs).
  - `POST /api/v1/incidents/{id}/postmortem` → runs the chat provider over the prompt, persists + returns; `GET` returns stored; `PUT` saves edits. Tenant-scoped, RBAC-gated (editor+).
  - Degrade: if no LLM provider, generate a deterministic template postmortem from the evidence (never fail).
- **Frontend:** IncidentsView "Postmortem" tab — Generate button → editable Markdown → Save + Export (client-side download/print).
- **Parity:** 3 routes consumed by the tab.
- **Tests:** `buildPostmortemPrompt` (sections present, timeline ordered); deterministic-fallback path; e2e generates/saves.

## E2 — Similar past incidents · M
- Pure `incidentFingerprint(services, rootCause) string` + `similarity(a,b)` (Jaccard on services + root-cause token overlap). `GET /incidents/{id}/similar` ranks resolved incidents. FE sidebar. Migration: none (compute over existing rows), optionally index.

## E3 — Assignment, ownership & escalation · M
- `incident_assignments(incident_id, assignee, status, escalated_at)` + status enum (ack/investigating/mitigated/resolved). `PATCH /incidents/{id}` (assignee/status). Escalation timer → notification-service. FE assignee picker + status stepper.

## E4 — MTTR/MTTA analytics · M
- `GET /incidents/analytics?from=&to=` aggregating MTTA/MTTR, counts by service/severity from incidents + timeline. Pure `computeMTTR(events)`. FE overview header with trend charts.

## E5 — Two-way comms · M
- Slack interactive callbacks (notification-service) → incident state; "war room" link. Depends on E3.

## E6 — Blast-radius & impact · S
- Join topology downstream + RUM/error counts into the incident payload; pure `impactScore(...)`. FE impact column + sort.

## Sequencing & gates
E1 → E3 → E4 → E2 → E6 → E5. Per slice: correlation build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(incidents): …`.
