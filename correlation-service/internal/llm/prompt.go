package llm

import (
	"encoding/json"
	"strings"
)

// chatSystemPrompt instructs the model how to return Action Cards and data
// queries. Two distinct tag protocols, checked for by ParseResponse in this
// priority order (query first): <query> asks the caller to fetch real data
// before answering — it should never be combined with prose that pretends to
// already know the answer. <action> proposes a remediation the user must
// confirm. A plain response with neither tag is a normal conversational answer.
//
// This lives here rather than on any one provider because every provider in
// the chain must speak the identical protocol: if Anthropic emitted <query>
// but the Ollama fallback did not, a failover would silently downgrade the
// natural-language query experience into a model guessing at telemetry numbers.
const chatSystemPrompt = `You are PulseTrace AI, an expert Site Reliability Engineer (SRE) embedded in an observability platform.

You do NOT have live telemetry memorized — you must ask for it. If the user asks a question that requires looking at real logs, traces, or metrics (e.g. "how many errors did checkout-service have?", "show me the p99 latency for cart-service", "search logs for timeout"), respond with ONLY a JSON block formatted exactly like this and nothing else:
<query>
{"tool": "search_logs", "args": {"service": "checkout-service", "level": "ERROR", "q": ""}}
</query>
Valid tools and their args:
- search_logs: args are service, level, trace_id, q (free text) — all optional.
- search_traces: args are service, route, interval (one of 1h/24h/7d) — all optional.
- query_metric: args are metric (required, exact metric name), type (gauge or sum, default gauge), service, interval (1h/24h/7d).
Do not guess at a metric name if you don't know one exists — ask the user to clarify, or use search_logs/search_traces instead, if you're not sure the metric exists.

If the user asks for a remediation action (like rollback, restart, or scale), respond with a JSON block at the very end of your message formatted exactly like this:
<action>
{"title": "Execute Rollback", "description": "Rollback the service", "actionLabel": "Confirm", "type": "ROLLBACK", "target": "service-name"}
</action>

If you were just given the results of a query (a "Query result:" block in the conversation), answer the user's original question using only that data — do not re-issue another <query> tag, and do not claim numbers that aren't in the provided result.

Otherwise, just answer their question concisely.`

// ParseResponse extracts the <query> and <action> protocol blocks from a raw
// model completion. Shared by every Provider implementation so that a
// failover between providers doesn't change how responses are interpreted.
//
// Malformed blocks degrade to plain text rather than erroring: a model that
// emits an unparseable <action> block should produce a chatty answer, not a
// failed request.
func ParseResponse(content string) Response {
	resp := Response{Text: content}

	// <query> is checked first and, when present, takes over the whole
	// response — the model was told not to mix a query request with prose
	// claiming to already know the answer, so there's no meaningful "text"
	// to keep in that case.
	if qStart := strings.Index(content, "<query>"); qStart != -1 {
		if qEnd := strings.Index(content, "</query>"); qEnd != -1 && qEnd > qStart {
			jsonStr := strings.TrimSpace(content[qStart+len("<query>") : qEnd])
			var q QueryRequest
			if err := json.Unmarshal([]byte(jsonStr), &q); err == nil && q.Tool != "" {
				resp.Query = &q
				resp.Text = ""
				return resp
			}
		}
	}

	startIdx := strings.Index(content, "<action>")
	endIdx := strings.Index(content, "</action>")

	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		jsonStr := content[startIdx+len("<action>") : endIdx]
		var action Action
		if err := json.Unmarshal([]byte(strings.TrimSpace(jsonStr)), &action); err == nil {
			resp.Action = &action
			resp.Text = strings.TrimSpace(content[:startIdx]) // Remove the action block from text
		}
	}
	return resp
}
