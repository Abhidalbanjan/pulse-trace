# AI SRE (Home) — Implementation Plan

Spec: [../ai-sre.md](../ai-sre.md) · Service: **correlation-service** (chat + query executor) · View: `frontend/src/app/page.tsx`

## Current state (grounded)
- `POST /api/v1/chat` (correlation ChatHandler) runs `search_logs`/`search_traces`/`query_metric` against the gateway via `internal/query/executor.go`; `POST /api/v1/actions/execute`. `FallbackProvider` chat chain.

## E4 — Grounded suggested prompts · S  *(recommended first slice)*
- `GET /api/v1/chat/suggestions` → chips seeded from live state (open incidents, recent deploys, anomalies) via existing endpoints. Pure `buildSuggestions(incidents, deploys, anomalies) []string`. FE renders chips on the empty state. Parity: route consumed.
- Tests: `buildSuggestions` prioritization; e2e chips render.

## E2 — Tool-call transparency + citations · M
- Chat response returns structured `tool_calls[]{name, args, result_summary, deep_link}`. FE collapsible "how I got this" + citation chips → `/explorer`, `/traces`. Backend: executor already runs tools — surface the invocation records.

## E1 — Streaming · M
- Chat handler streams tokens (SSE `text/event-stream`); gateway proxy passes through. FE consumes stream (stop/regenerate). Keep non-stream endpoint for compatibility.

## E3 — Conversation history · M
- **Data (correlation migration 006):** `chat_conversations(id, tenant_id, user, title, created_at)`, `chat_messages(id, conversation_id, role, content, tool_calls JSONB, created_at)`. CRUD `GET/POST /api/v1/chat/conversations[/{id}]`. FE left-rail threads; multi-turn context.

## E5 — Copilot actions with human-in-the-loop · M
- Proposed remediation → `PlaybookAction` under the existing remediation policy; FE approval card (reuse RemediationPanel). Never auto-execute.

## E6 — Provider-health & cost footer · S
- Reuse `/api/v1/causal/providers`; per-response latency/model footer.

## Sequencing & gates
E4 → E2 → E1 → E3 → E5 → E6. Per slice: correlation build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(ai-sre): …`.
