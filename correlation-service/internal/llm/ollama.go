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
	
	// Add system prompt to instruct the LLM on how to return Action Cards
	sysPrompt := ollamaMessage{
		Role: "system",
		Content: `You are PulseTrace AI, an expert Site Reliability Engineer (SRE).
If a user asks for a remediation action (like rollback, restart, or scale), respond with a JSON block at the very end of your message formatted exactly like this:
<action>
{"title": "Execute Rollback", "description": "Rollback the service", "actionLabel": "Confirm", "type": "ROLLBACK", "target": "service-name"}
</action>
Otherwise, just answer their question concisely based on telemetry data.`,
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
