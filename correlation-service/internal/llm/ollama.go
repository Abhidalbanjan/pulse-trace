package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ollamaTimeout bounds a single Ollama completion. Without it a wedged local
// daemon would hang the chat request indefinitely — and, once Ollama is the
// last link in a fallback chain, would hang every chat request behind it.
const ollamaTimeout = 120 * time.Second

type OllamaProvider struct {
	Endpoint string
	Model    string
	client   *http.Client
}

func NewOllamaProvider(endpoint, model string) *OllamaProvider {
	if endpoint == "" {
		endpoint = "http://host.docker.internal:11434" // Default for docker communicating with host ollama
	}
	if model == "" {
		model = "llama3" // default
	}
	return &OllamaProvider{
		Endpoint: endpoint,
		Model:    model,
		client:   &http.Client{Timeout: ollamaTimeout},
	}
}

// Name identifies this link in logs and in fallback-chain descriptions.
func (o *OllamaProvider) Name() string { return "ollama/" + o.Model }

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

	reqBody.Messages = append(reqBody.Messages, ollamaMessage{Role: "system", Content: chatSystemPrompt})
	for _, m := range messages {
		reqBody.Messages = append(reqBody.Messages, ollamaMessage{Role: m.Role, Content: m.Content})
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return Response{}, err
	}

	// The request is built with the caller's context so that a cancelled or
	// timed-out chat request actually tears down the in-flight completion,
	// and so a fallback router can abandon this link on its own deadline.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/chat", o.Endpoint), bytes.NewReader(jsonData))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := o.client
	if client == nil {
		// Zero-value OllamaProvider (constructed as a literal rather than via
		// NewOllamaProvider) still needs a bounded client.
		client = &http.Client{Timeout: ollamaTimeout}
	}

	resp, err := client.Do(req)
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

	return ParseResponse(oResp.Message.Content), nil
}
