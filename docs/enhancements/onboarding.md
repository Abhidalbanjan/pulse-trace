# Onboarding — Enhancement Spec

**Route:** `/onboarding` · **Component:** `frontend/src/components/Onboarding/Wizard.tsx` · **Backend:** gateway ingestion keys (`/api/v1/admin/ingestion-keys`), OTLP/migration ingestion paths

## 1. Where it stands

- A wizard that mints an ingestion key.
- The backend accepts OTLP (traces/metrics/logs) plus Datadog/Splunk "Trojan Horse" migration ingestion.

## 2. Market-ready gap

Time-to-first-data is the single biggest driver of observability adoption. Today the user gets a key and is on their own. Market leaders hand you **copy-paste instrumentation per language/framework**, **detect the first datapoint live**, and walk you to your first dashboard and alert. This is where trials convert or die.

## 3. Proposed enhancements

### E1. Guided instrumentation snippets · **M**
- **User value:** copy-paste to send data in under 5 minutes.
- **What:** pick language/framework (Node, Python, Go, Java, browser RUM, k8s, Docker) → exact OTel SDK/agent snippet pre-filled with the endpoint + the minted key. Include the Datadog/Splunk **migration** path ("keep your agent, point it here").
- **Backend:** none new — templated client-side, key injected.
- **Frontend:** language tabs + copy buttons + curl "send a test event" one-liner.

### E2. Live "first data" detector · **M**
- **User value:** instant "✅ we received your first trace!" — the dopamine that converts trials.
- **What:** poll for first ingestion on the tenant; advance the wizard automatically when data arrives.
- **Backend:** `GET /api/v1/onboarding/status` (first-seen per signal from metering counters).
- **Frontend:** live checklist that lights up per signal (logs ✓, traces ✓, metrics …).

### E3. Onboarding checklist to value · **S**
- **User value:** a clear path: send data → see it → create an SLO → set an alert → invite your team.
- **What:** a persistent checklist with deep links and completion state.
- **Backend:** derive completion from existing resources (keys, data, SLOs, alerts, users).
- **Frontend:** checklist card on home until complete.

### E4. One-click demo data / sample app · **S**
- **User value:** explore the product before wiring real services.
- **What:** a "Load sample data" button (server-side seed) so an evaluator sees a populated product immediately.
- **Backend:** a guarded, tenant-scoped seed endpoint (admin only).
- **Frontend:** "Try with sample data" on the empty state.

### E5. Team invite step · **S**
- **User value:** observability is a team sport; invite during setup.
- **What:** invite teammates by email/role as a wizard step (reuses user creation / future invite flow).
- **Backend:** invite endpoint (or user create) + optional email via the F18 mailer.
- **Frontend:** invite step with role picker.

### E6. Integration catalog · **M**
- **User value:** discover what PulseTrace connects to (k8s, Slack, PagerDuty, GitHub, Stripe…).
- **What:** a browsable catalog of integrations with setup guides, linking to Settings.
- **Frontend:** integrations grid; backend links to existing config surfaces.

## 4. Market-ready DoD

- A new user goes from signup to first trace/log/metric with copy-paste snippets and sees it confirmed live.
- A checklist walks them to their first SLO, alert, and team invite; evaluators can load sample data with one click.

## 5. Suggested sequence

E1 (snippets) → E2 (first-data detector) → E3 (checklist) → E4 (demo data) → E5 (invite) → E6 (integration catalog).
