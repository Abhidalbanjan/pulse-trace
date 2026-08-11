# AI SRE (Home) — Enhancement Spec

**Route:** `/` · **Component:** `frontend/src/app/page.tsx` · **Backend:** `correlation-service` chat handler + query executor, `gateway /api/v1/chat`, `/api/v1/actions/execute`

## 1. Where it stands

- A natural-language chat that answers questions by running **real queries**
  (`search_logs` / `search_traces` / `query_metric`) against the gateway via
  `correlation-service/internal/query/executor.go`, so the LLM narrates real
  telemetry instead of fabricating it.
- Can execute an action (`/api/v1/actions/execute`).
- Backed by the `FallbackProvider` LLM chain with per-provider health.

## 2. Market-ready gap

The wedge of the whole product is "AI SRE," yet the home experience is a
single-shot Q&A box. Buyers comparing against Datadog Bits / New Relic AI expect
a **streaming, transparent, stateful copilot**: they can see what it looked at,
trust it, follow up, and come back to the conversation.

## 3. Proposed enhancements

### E1. Streaming responses — no more staring at a spinner · **M**
- **User value:** answers feel instant; long RCA narratives stream token-by-token.
- **What:** SSE/streaming endpoint from the chat handler; the UI renders partial tokens.
- **Backend:** `correlation-service` chat handler → stream chunks; gateway proxy passes through (`text/event-stream`).
- **Frontend:** consume the stream, append tokens; stop/regenerate controls.

### E2. Tool-call transparency + citations · **M**
- **User value:** trust. The copilot shows *"ran search_logs(service=payment, level=ERROR, 15m) → 240 hits"* and links each claim to the Explorer/Traces view that produced it.
- **What:** surface the executor's tool calls + args + result summaries as inline "steps"; deep-link citations.
- **Backend:** return structured tool-invocation records alongside the answer.
- **Frontend:** collapsible "how I got this" panel; citation chips → `/explorer?q=…`, `/traces?trace=…`.

### E3. Conversation history & persistence · **M**
- **User value:** pick up where you left off; share a thread with a teammate.
- **What:** persist conversations (per user/tenant), list past threads, resume with context.
- **Backend:** `chat_conversations` + `chat_messages` tables (migration); `GET/POST /api/v1/chat/conversations`.
- **Frontend:** left rail of past conversations; multi-turn context sent to the model.

### E4. Grounded suggested prompts · **S**
- **User value:** a blank box is intimidating; grounded chips invite action.
- **What:** dynamic starter chips from live state — *"Why is checkout p99 up?"*, *"Summarize the 2 open incidents"*, *"What deployed in the last hour?"*.
- **Backend:** reuse `/api/v1/incidents`, deployments, metrics anomalies to seed suggestions.
- **Frontend:** render chips; clicking runs the prompt.

### E5. Copilot-initiated actions with human-in-the-loop · **M**
- **User value:** "restart the pool" from chat, but safely.
- **What:** when the model proposes a remediation, render it as an **approval card** (reuse the F1 RemediationPanel policy: dry-run / approve / reject), never auto-execute.
- **Backend:** map proposed action → `PlaybookAction` under the existing remediation policy.
- **Frontend:** action card with the same guardrails as Incidents.

### E6. Provider-health & cost surfacing · **S**
- **User value:** operators know which model answered and that the AI is live.
- **What:** reuse `/api/v1/causal/providers` badge here; optional per-response latency/token cost.
- **Frontend:** small "answered by <model> · 1.2s" footer under each answer.

## 4. Market-ready DoD

- Answers stream; the user can see the exact queries/tools the AI ran and click through to the evidence.
- Conversations persist and are resumable; multi-turn context works.
- Any action the copilot proposes goes through the same human-in-the-loop policy as Incidents.
- Grounded suggestions make the empty state productive.

## 5. Suggested sequence

E4 (quick win) → E2 (trust) → E1 (streaming) → E3 (history) → E5 (actions) → E6 (polish).
