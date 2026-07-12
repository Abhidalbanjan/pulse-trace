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

// Response represents the LLM response, optionally including an actionable runbook
type Response struct {
	Text   string  `json:"text"`
	Action *Action `json:"actionCard,omitempty"`
}

// Provider is the interface for any LLM backend (Ollama, OpenAI, Anthropic, etc.)
type Provider interface {
	Chat(ctx context.Context, messages []Message) (Response, error)
}
