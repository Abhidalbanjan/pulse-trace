package causal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/llms/openai"

	"github.com/pulsetrace/shared/models"
)

const (
	defaultMaxTokens = 1024
)

// LangChainAnalyzer calls the LangChain Go library to support multiple LLM providers.
type LangChainAnalyzer struct {
	provider  string
	modelName string
	model     llms.Model
}

// NewLangChainAnalyzer creates a factory that initializes the appropriate LangChain model client based on the provider string.
func NewLangChainAnalyzer(provider, modelName string) (*LangChainAnalyzer, error) {
	ctx := context.Background()
	var model llms.Model
	var err error

	provider = strings.ToLower(strings.TrimSpace(provider))

	switch provider {
	case "openai":
		var opts []openai.Option
		if modelName != "" {
			opts = append(opts, openai.WithModel(modelName))
		}
		endpoint := os.Getenv("LLM_ENDPOINT")
		if endpoint != "" {
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
		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey != "" {
			opts = append(opts, googleai.WithAPIKey(apiKey))
		}
		if modelName != "" {
			opts = append(opts, googleai.WithDefaultModel(modelName))
		}
		model, err = googleai.New(ctx, opts...)

	case "ollama":
		var opts []ollama.Option
		if modelName != "" {
			opts = append(opts, ollama.WithModel(modelName))
		}
		endpoint := os.Getenv("LLM_ENDPOINT")
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		opts = append(opts, ollama.WithServerURL(endpoint))
		model, err = ollama.New(opts...)

	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", provider)
	}

	if err != nil {
		return nil, fmt.Errorf("initialize %s provider: %w", provider, err)
	}

	return &LangChainAnalyzer{
		provider:  provider,
		modelName: modelName,
		model:     model,
	}, nil
}

func (l *LangChainAnalyzer) Name() string {
	if l.modelName != "" {
		return l.modelName
	}
	return l.provider
}

// Analyze sends the evidence structure using standard LangChain chat interfaces.
func (l *LangChainAnalyzer) Analyze(ctx context.Context, e *Evidence) (*models.CausalAnalysis, error) {
	deterministicChain := BuildChain(e.Alerts, e.Dependencies)

	systemPromptText := systemPrompt + "\n\n" + renderDependencyGraph(e.Dependencies)
	userPayload := renderEvidence(e, deterministicChain)

	content := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPromptText),
		llms.TextParts(llms.ChatMessageTypeHuman, userPayload),
	}

	resp, err := l.model.GenerateContent(ctx, content, llms.WithMaxTokens(defaultMaxTokens))
	if err != nil {
		return nil, fmt.Errorf("langchain generate: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty model response choices")
	}

	text := resp.Choices[0].Content
	if text == "" {
		return nil, fmt.Errorf("empty model response text")
	}

	parsed, err := parseModelJSON(text)
	if err != nil {
		return nil, fmt.Errorf("parse model JSON: %w (raw response: %q)", err, text)
	}

	chain := parsed.Chain
	if len(chain) == 0 {
		chain = deterministicChain
	}

	return &models.CausalAnalysis{
		Chain:      chain,
		Narrative:  parsed.Narrative,
		RootCause:  parsed.RootCause,
		Confidence: clamp01(parsed.Confidence),
		Model:      l.Name(),
		AnalyzedAt: time.Now().UTC(),
	}, nil
}

// ── Prompt & Payload Rendering Utilities ───────────────────────────────────

const systemPrompt = `You are the causal root-cause analyst for PulseTrace, a distributed observability platform.

You are given:
  1. A static map of inter-service dependencies (cached).
  2. An incident composed of one or more alerts, each with a timestamp, service, level, and message.
  3. A deterministic causal chain pre-computed from the dependency graph.

Your task: refine the chain into a most-likely causal hypothesis and write a concise narrative an on-call SRE can act on.

Output strict JSON only, no prose outside the JSON, matching exactly this shape:
{
  "chain": [
    {"from_service":"<svc>", "to_service":"<svc>", "evidence":"<one sentence>", "at":"<RFC3339>"}
  ],
  "narrative": "<2-4 sentences explaining the likely causal story>",
  "root_cause": "<single sentence — the hypothesized root cause>",
  "confidence": <float 0.0 to 1.0>
}

Rules:
  - Confidence reflects the strength of evidence, not your eloquence. With one alert and no upstream signal, confidence should be < 0.5.
  - Do not invent services not present in the dependency graph.
  - Order the chain upstream → downstream (cause first, symptom last).
  - The narrative must reference specific service names and timestamps, not generic phrases.`

func renderDependencyGraph(deps map[string][]string) string {
	if len(deps) == 0 {
		return "Service dependency graph: (none provided)"
	}
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("Service dependency graph (downstream → upstream):\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s → [%s]\n", k, strings.Join(deps[k], ", "))
	}
	return b.String()
}

func renderEvidence(e *Evidence, chain []models.CausalLink) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Incident %s — %s\n", e.Incident.ID, e.Incident.Title)
	fmt.Fprintf(&b, "Severity: %s | Status: %s | Alerts: %d | Window: %s\n\n",
		e.Incident.Severity, e.Incident.Status, e.Incident.AlertCount, e.Window)

	b.WriteString("Alerts (oldest first):\n")
	alerts := make([]models.IncidentAlert, len(e.Alerts))
	copy(alerts, e.Alerts)
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].TriggeredAt.Before(alerts[j].TriggeredAt) })
	for i, a := range alerts {
		fmt.Fprintf(&b, "  %d. [%s] %s @ %s — %s\n",
			i+1, a.Level, a.ServiceName, a.TriggeredAt.Format(time.RFC3339), a.Message,
		)
	}

	b.WriteString("\nDeterministic causal chain (from dependency graph + timing):\n")
	if len(chain) == 0 {
		b.WriteString("  (no upstream correlations found)\n")
	} else {
		for i, l := range chain {
			fmt.Fprintf(&b, "  %d. %s → %s @ %s — %s\n",
				i+1, l.FromService, l.ToService, l.At.Format(time.RFC3339), l.Evidence,
			)
		}
	}

	b.WriteString("\nReturn the refined causal hypothesis as strict JSON per the system instructions.")
	return b.String()
}

// ── Response parsing ───────────────────────────────────────────────────────

type modelOutput struct {
	Chain      []models.CausalLink `json:"chain"`
	Narrative  string              `json:"narrative"`
	RootCause  string              `json:"root_cause"`
	Confidence float64             `json:"confidence"`
}

func parseModelJSON(text string) (*modelOutput, error) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in response")
	}

	var out modelOutput
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
