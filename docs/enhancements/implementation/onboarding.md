# Onboarding — Implementation Plan

Spec: [../onboarding.md](../onboarding.md) · Service: **gateway** · View: `frontend/src/components/Onboarding/Wizard.tsx`

## Current state (grounded)
- Wizard mints an ingestion key (`/api/v1/admin/ingestion-keys`); backend accepts OTLP + Datadog/Splunk migration ingestion.

## E1 — Guided instrumentation snippets · M  *(recommended first slice — mostly FE)*
- FE: language/framework tabs (Node, Python, Go, Java, browser RUM, k8s, Docker) → OTel SDK/agent snippets pre-filled with the endpoint + minted key + a curl "send a test event" one-liner; include the Datadog/Splunk migration path. No backend change (templated client-side with the key).
- Tests: snippet templating unit; e2e tab switch + copy.

## E2 — Live "first data" detector · M
- `GET /api/v1/onboarding/status` → first-seen per signal (from metering counters, tenant-scoped). FE live checklist lights up per signal; advance the wizard on arrival. Parity: route consumed.
- Tests: status derivation; e2e checklist reacts.

## E3 — Onboarding checklist to value · S
- Derive completion (keys/data/SLOs/alerts/users) → checklist card on home with deep links.

## E4 — One-click demo data / sample app · S
- Guarded admin-only tenant-scoped seed endpoint; FE "Try with sample data" on empty states.

## E5 — Team invite step · S
- Invite by email/role (reuse user create + F18 mailer) as a wizard step.

## E6 — Integration catalog · M
- FE integrations grid linking to Settings config surfaces.

## Sequencing & gates
E1 → E2 → E3 → E4 → E5 → E6. Per slice: gateway build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(onboarding): …`.
