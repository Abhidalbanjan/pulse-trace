package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/pulsetrace/correlation-service/internal/llm"
	"github.com/pulsetrace/correlation-service/internal/query"
)

// ChatHandler powers PulseTrace's natural-language query experience: instead
// of building a bespoke PromQL/LogQL-style query language, the user asks a
// plain-English question and the LLM decides which real backend query to
// run (via query.Executor, against gateway-service's existing
// logs/traces/metrics endpoints) before answering — the model only ever
// summarizes data that was actually fetched, never invents numbers.
type ChatHandler struct {
	provider llm.Provider
	executor *query.Executor
}

func NewChatHandler(provider llm.Provider, executor *query.Executor) *ChatHandler {
	return &ChatHandler{provider: provider, executor: executor}
}

func (h *ChatHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/chat", h.HandleChat)
}

type ChatRequest struct {
	Message string `json:"message"`
}

// maxQueryHops bounds how many times a single request will let the model
// ask for data before giving up and answering with whatever it has — a
// buggy or adversarial prompt asking for query after query should never
// turn into an unbounded loop of gateway calls.
const maxQueryHops = 2

func (h *ChatHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	// Forward the caller's own bearer token to gateway-service for any tool
	// query, so the LLM can never see more than the asking user is already
	// authorized to see.
	token := bearerToken(r)

	messages := []llm.Message{
		{Role: "user", Content: req.Message},
	}

	// Tool-call transparency (AI-SRE · E2): record each backend query the model
	// ran, so the answer can show "how I got this" with citation deep-links
	// instead of asking the user to trust an unsourced number.
	var toolCalls []toolCall

	var resp llm.Response
	for hop := 0; hop <= maxQueryHops; hop++ {
		var err error
		resp, err = h.provider.Chat(r.Context(), messages)
		if err != nil {
			log.Printf("chat: LLM provider unavailable: %v", err)
			http.Error(w, "the AI assistant is temporarily unavailable — the LLM backend could not be reached", http.StatusServiceUnavailable)
			return
		}

		if resp.Query == nil {
			break
		}
		if h.executor == nil {
			resp.Text = "I'd need to look up live data to answer that, but this deployment doesn't have query execution configured."
			resp.Query = nil
			break
		}

		result, err := h.executor.Run(r.Context(), token, resp.Query.Tool, resp.Query.Args)
		if err != nil {
			log.Printf("chat: tool query %q failed: %v", resp.Query.Tool, err)
			result = "The query failed: " + err.Error()
		}
		toolCalls = append(toolCalls, toolCall{
			Name:          resp.Query.Tool,
			Args:          resp.Query.Args,
			ResultSummary: summarizeToolResult(result),
			DeepLink:      toolDeepLink(resp.Query.Tool, resp.Query.Args),
		})

		// Feed the result back as the next turn so the model's second
		// completion is grounded in what was actually returned, not asked
		// to hallucinate a continuation of its own tool call.
		messages = append(messages,
			llm.Message{Role: "assistant", Content: "(requested " + resp.Query.Tool + ")"},
			llm.Message{Role: "user", Content: "Query result:\n" + result + "\n\nNow answer my original question using only this data."},
		)
	}

	// If we exhausted maxQueryHops and the model is still asking for another
	// query, don't ship an empty-text response to the frontend — answer with
	// whatever the last query actually returned instead of silently failing.
	if resp.Query != nil {
		resp.Text = "I gathered some data but couldn't fully resolve this in the allotted number of lookups. Try asking a more specific question (e.g. name a specific service)."
		resp.Query = nil
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatResponse{Text: resp.Text, Action: resp.Action, ToolCalls: toolCalls})
}

// toolCall is a transparent record of one backend query the assistant ran.
type toolCall struct {
	Name          string            `json:"name"`
	Args          map[string]string `json:"args"`
	ResultSummary string            `json:"result_summary"`
	DeepLink      string            `json:"deep_link,omitempty"`
}

// chatResponse is the wire shape returned to the frontend: the model's text and
// action card (as before), plus the tool-call trail (AI-SRE · E2).
type chatResponse struct {
	Text      string      `json:"text"`
	Action    *llm.Action `json:"actionCard,omitempty"`
	ToolCalls []toolCall  `json:"tool_calls,omitempty"`
}

// summarizeToolResult compresses a raw tool result into a one-line citation
// preview: whitespace collapsed and truncated. Pure.
func summarizeToolResult(result string) string {
	s := strings.Join(strings.Fields(result), " ")
	const max = 180
	if len(s) > max {
		s = s[:max] + "…"
	}
	if s == "" {
		return "(no data returned)"
	}
	return s
}

// toolDeepLink maps a tool invocation to the in-app screen that shows the same
// data, so a citation chip is clickable ("see the logs/traces/metrics I used").
// Pure.
func toolDeepLink(tool string, args map[string]string) string {
	switch tool {
	case query.ToolSearchLogs:
		q := url.Values{}
		if v := args["q"]; v != "" {
			q.Set("q", v)
		}
		if v := args["service"]; v != "" {
			q.Set("service", v)
		}
		if enc := q.Encode(); enc != "" {
			return "/explorer?" + enc
		}
		return "/explorer"
	case query.ToolSearchTraces:
		if v := args["service"]; v != "" {
			return "/traces?service=" + url.QueryEscape(v)
		}
		return "/traces"
	case query.ToolQueryMetric:
		return "/metrics"
	default:
		return ""
	}
}

// bearerToken extracts the raw token from an "Authorization: Bearer <token>"
// header, or "" if absent — matching how gateway-service's own AuthMiddleware
// expects tokens to be presented.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if strings.HasPrefix(h, prefix) {
		return strings.TrimPrefix(h, prefix)
	}
	return ""
}
