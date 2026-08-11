# Incidents — Enhancement Spec

**Route:** `/incidents` · **Component:** `frontend/src/components/Incidents/IncidentsView.tsx` · **Backend:** `correlation-service` (correlator, causal analyzer, playbooks)

## 1. Where it stands

- Real incidents from the alert→correlation pipeline, with a **causal-AI narrative**, causal chain, confidence, and a **hallucination-guardrail badge** (Grounded / Adjusted, F15).
- **Provider-health badge** for the causal AI (F15).
- Human-in-the-loop **RemediationPanel** (dry-run / approve / reject under a policy), timeline, affected services.

## 2. Market-ready gap

Detection + RCA are strong; the **incident-management lifecycle** around them is thin. PagerDuty/incident.io buyers expect assignment, comms, similar-incident recall, postmortems, and MTTR analytics — the things that make an incident tool the team's operational home, not just a viewer.

## 3. Proposed enhancements

### E1. AI-drafted postmortem · **M**
- **User value:** the retro writes itself — timeline, root cause, impact, action items — in one click.
- **What:** "Generate postmortem" produces a structured doc from the incident's timeline + causal analysis + linked alerts/deploys; editable; exportable (Markdown/PDF).
- **Backend:** `POST /api/v1/incidents/{id}/postmortem` (LLM over the assembled evidence, reusing the causal Evidence bundle); persist as `incident_postmortems`.
- **Frontend:** postmortem tab with edit + export.

### E2. Similar past incidents · **M**
- **User value:** *"This looks like INC-142 three weeks ago — here's what fixed it."*
- **What:** match the new incident against resolved ones by service set + root-cause fingerprint; show the closest matches and their resolution/playbook.
- **Backend:** a similarity query (fingerprint from services + causal root cause); `GET /api/v1/incidents/{id}/similar`.
- **Frontend:** "Similar incidents" sidebar with resolution snippets.

### E3. Assignment, ownership & escalation · **M**
- **User value:** every incident has an owner; escalation is visible.
- **What:** assign to a user/team, status transitions (acknowledged/investigating/mitigated/resolved), on-call from Catalog/PagerDuty; escalation timer.
- **Backend:** `incident_assignments`; PATCH incident state; wire to notification-service for escalation.
- **Frontend:** assignee picker, status stepper, escalation banner.

### E4. MTTR / MTTA analytics · **M**
- **User value:** leadership sees reliability trending; SREs see where time goes.
- **What:** an incidents-overview dashboard — MTTA, MTTR, incident count by service/severity, time-to-detect vs time-to-resolve, top offenders.
- **Backend:** aggregation over incidents + timeline events; `GET /api/v1/incidents/analytics`.
- **Frontend:** overview header with trend charts + filters.

### E5. Two-way comms integration · **M**
- **User value:** run the incident from Slack; acks and status sync back.
- **What:** post incident to a Slack channel, allow ack/resolve from Slack (or from the AI SRE chat), reflect state in the UI.
- **Backend:** notification-service Slack interactive callbacks → incident state.
- **Frontend:** "War room" link + synced status.

### E6. Blast-radius & impact scoring · **S**
- **User value:** triage by real impact, not just severity.
- **What:** compute impact from downstream topology + affected RUM sessions/tenants; show an impact score on the list.
- **Backend:** join topology downstream + RUM/error counts into the incident payload.
- **Frontend:** impact column + sort.

## 4. Market-ready DoD

- Every incident can be assigned, has a status lifecycle, and shows similar past incidents and blast radius.
- One-click AI postmortem that a human can edit and export.
- An analytics view that answers "are we getting more reliable?" with MTTR/MTTA trends.

## 5. Suggested sequence

E1 (postmortem — demo gold) → E3 (lifecycle) → E4 (analytics) → E2 (similar) → E6 (impact) → E5 (comms).
