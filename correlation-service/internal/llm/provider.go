package llm

import "context"

// Message represents a chat message
type Message struct {
	Role    string
	Content string
}

// Action represents a parsed remediation action returned by the LLM
type Action struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	ActionLabel string `json:"actionLabel"`
	Type        string `json:"type"` // e.g., ROLLBACK, RESTART, SCALE
	Target      string `json:"target"`
}

// QueryRequest is a tool call the LLM asks the caller to run on its behalf
// before it can answer — e.g. "search_logs" with args {"service": "checkout"}.
// This is what makes natural-language queries real: the model never
// fabricates telemetry data, it asks for the query it needs, the caller
// executes it against real ClickHouse/Quickwit-backed endpoints
// (see internal/query/executor.go), and feeds the result back for a second,
// grounded completion.
type QueryRequest struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args"`
}

// Response represents the LLM response, optionally including an actionable
// runbook (Action) or a data query it needs run before it can answer (Query).
// A single response should carry at most one of Action/Query in practice —
// the system prompt instructs the model accordingly — but both are separate
// optional fields rather than a union type, since json.Unmarshal handles
// "the tag wasn't present" more simply as a nil pointer than a variant.
type Response struct {
	Text   string        `json:"text"`
	Action *Action       `json:"actionCard,omitempty"`
	Query  *QueryRequest `json:"-"`
}

// Provider is the interface for any LLM backend (Ollama, OpenAI, Anthropic, etc.)
//
// Implementations must be safe for concurrent use — a single Provider is
// shared by the chat handler and the SLO handler across all in-flight
// requests.
type Provider interface {
	Chat(ctx context.Context, messages []Message) (Response, error)

	// Name identifies the backend for logs and for describing a fallback
	// chain, e.g. "anthropic/claude-sonnet-4-5" or "ollama/llama3". It must
	// not include credentials.
	Name() string
}
