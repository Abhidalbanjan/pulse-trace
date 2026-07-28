package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/llms/openai"
)

// chatMaxTokens bounds a single chat completion. Chat answers are short by
// design (the system prompt asks for concise answers or a single JSON block),
// so this is a cost guard, not a quality constraint.
const chatMaxTokens = 1024

// LangChainProvider adapts a langchaingo llms.Model to the Provider
// interface, giving the chat and SLO surfaces the same hosted-model options
// the causal-RCA path already had.
//
// Before this, the chat surface was hardcoded to a local Ollama daemon: a
// deployment with an Anthropic key still got llama3 answering natural-language
// telemetry questions, and a SaaS deployment with no Ollama daemon at all got
// nothing. The protocol (chatSystemPrompt + ParseResponse) is shared with
// OllamaProvider so every backend behaves identically.
type LangChainProvider struct {
	provider  string
	modelName string
	model     llms.Model
}

// NewLangChainProvider builds a provider for one of: openai, anthropic,
// googleai, ollama. modelName may be empty to use the backend's default.
//
// langchaingo's constructors validate credentials eagerly, so a missing or
// malformed API key fails here at startup rather than on a user's first
// question.
func NewLangChainProvider(provider, modelName string) (*LangChainProvider, error) {
	var model llms.Model
	var err error

	provider = strings.ToLower(strings.TrimSpace(provider))
	modelName = strings.TrimSpace(modelName)

	switch provider {
	case "openai":
		var opts []openai.Option
		if modelName != "" {
			opts = append(opts, openai.WithModel(modelName))
		}
		if endpoint := providerEndpoint("OPENAI_BASE_URL"); endpoint != "" {
			opts = append(opts, openai.WithBaseURL(endpoint))
		}
		model, err = openai.New(opts...)

	case "anthropic":
		var opts []anthropic.Option
		if modelName != "" {
			opts = append(opts, anthropic.WithModel(modelName))
		}
		model, err = anthropic.New(opts...)

	case "googleai":
		var opts []googleai.Option
		if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
			opts = append(opts, googleai.WithAPIKey(apiKey))
		}
		if modelName != "" {
			opts = append(opts, googleai.WithDefaultModel(modelName))
		}
		model, err = googleai.New(context.Background(), opts...)

	case "ollama":
		var opts []ollama.Option
		if modelName != "" {
			opts = append(opts, ollama.WithModel(modelName))
		}
		endpoint := providerEndpoint("OLLAMA_ENDPOINT")
		if endpoint == "" {
			endpoint = "http://host.docker.internal:11434"
		}
		opts = append(opts, ollama.WithServerURL(endpoint))
		model, err = ollama.New(opts...)

	default:
		return nil, fmt.Errorf("unsupported LLM provider: %q", provider)
	}

	if err != nil {
		return nil, fmt.Errorf("initialize %s provider: %w", provider, err)
	}

	return &LangChainProvider{provider: provider, modelName: modelName, model: model}, nil
}

func (p *LangChainProvider) Name() string {
	if p.modelName == "" {
		return p.provider
	}
	return p.provider + "/" + p.modelName
}

func (p *LangChainProvider) Chat(ctx context.Context, messages []Message) (Response, error) {
	content := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, chatSystemPrompt),
	}
	for _, m := range messages {
		content = append(content, llms.TextParts(chatRole(m.Role), m.Content))
	}

	resp, err := p.model.GenerateContent(ctx, content, llms.WithMaxTokens(chatMaxTokens))
	if err != nil {
		return Response{}, fmt.Errorf("%s generate: %w", p.Name(), err)
	}
	if len(resp.Choices) == 0 {
		return Response{}, fmt.Errorf("%s returned no choices", p.Name())
	}

	text := resp.Choices[0].Content
	if strings.TrimSpace(text) == "" {
		// An empty completion is treated as a failure rather than an empty
		// answer, so a fallback router moves on to the next provider instead
		// of surfacing a blank chat bubble to the user.
		return Response{}, fmt.Errorf("%s returned an empty completion", p.Name())
	}

	return ParseResponse(text), nil
}

// chatRole maps the platform's role strings onto langchaingo's chat message
// types. Anything unrecognized is treated as a human turn: the alternative
// (dropping it) would silently lose conversation context.
func chatRole(role string) llms.ChatMessageType {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "ai":
		return llms.ChatMessageTypeAI
	case "system":
		return llms.ChatMessageTypeSystem
	default:
		return llms.ChatMessageTypeHuman
	}
}

// providerEndpoint resolves a provider-specific endpoint override, falling
// back to the legacy shared LLM_ENDPOINT.
//
// LLM_ENDPOINT is ambiguous once providers are chained — a value meant to
// point Ollama at a local daemon would also be handed to OpenAI as its base
// URL. Provider-specific vars win; LLM_ENDPOINT stays as a fallback so
// existing single-provider deployments keep working unchanged.
func providerEndpoint(specificVar string) string {
	if v := strings.TrimSpace(os.Getenv(specificVar)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("LLM_ENDPOINT"))
}
