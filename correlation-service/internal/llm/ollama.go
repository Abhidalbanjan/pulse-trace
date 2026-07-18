package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type OllamaProvider struct {
	Endpoint string
	Model    string
}

func NewOllamaProvider(endpoint, model string) *OllamaProvider {
	if endpoint == "" {
		endpoint = "http://host.docker.internal:11434" // Default for docker communicating with host ollama
	}
	if model == "" {
		model = "llama3" // default
	}
	return &OllamaProvider{Endpoint: endpoint, Model: model}
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type ollamaResponse struct {
	Message ollamaMessage `json:"message"`
}

func (o *OllamaProvider) Chat(ctx context.Context, messages []Message) (Response, error) {
	reqBody := ollamaRequest{
		Model:  o.Model,
		Stream: false,
	}
	
	// Add system prompt to instruct the LLM on how to return Action Cards and
	// data queries. Two distinct tag protocols, checked for by the caller in
	// this priority order (query first): <query> asks the caller to fetch
	// real data before answering — it should never be combined with prose
	// that pretends to already know the answer. <action> proposes a
	// remediation the user must confirm. A plain response with neither tag
	// is a normal conversational answer.
	sysPrompt := ollamaMessage{
		Role: "system",
		Content: `You are PulseTrace AI, an expert Site Reliability Engineer (SRE) embedded in an observability platform.

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

Otherwise, just answer their question concisely.`,
	}
	reqBody.Messages = append(reqBody.Messages, sysPrompt)

	for _, m := range messages {
		reqBody.Messages = append(reqBody.Messages, ollamaMessage{Role: m.Role, Content: m.Content})
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return Response{}, err
	}

	resp, err := http.Post(fmt.Sprintf("%s/api/chat", o.Endpoint), "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return Response{}, fmt.Errorf("failed to reach Ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("ollama returned status: %d", resp.StatusCode)
	}

	var oResp ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&oResp); err != nil {
		return Response{}, err
	}

	return o.parseResponse(oResp.Message.Content), nil
}

func (o *OllamaProvider) parseResponse(content string) Response {
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
		jsonStr := content[startIdx+8 : endIdx]
		var action Action
		if err := json.Unmarshal([]byte(strings.TrimSpace(jsonStr)), &action); err == nil {
			resp.Action = &action
			resp.Text = strings.TrimSpace(content[:startIdx]) // Remove the action block from text
		}
	}
	return resp
}
