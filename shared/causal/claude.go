package causal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pulsetrace/shared/models"
)

const (
	anthropicEndpoint   = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
	defaultModel        = "claude-opus-4-7"
	defaultMaxTokens    = 1024
	defaultTimeout      = 30 * time.Second
)

// ClaudeAnalyzer calls the Anthropic Messages API to produce a causal
// narrative + refined root-cause hypothesis for an incident.
//
// Design notes:
//   - The static system prompt (instructions + service dependency graph) is
//     marked cache_control: ephemeral so subsequent requests within the cache
//     window cost ~10% of the first call.
//   - The deterministic chain from BuildChain is included as evidence — the
//     model refines it rather than reasoning from scratch.
//   - The model is instructed to return a strict JSON object; we parse it
//     and fall back to a templated narrative on parse failure.
type ClaudeAnalyzer struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewClaudeAnalyzer returns an analyzer that calls Anthropic's API.
// model may be empty — it defaults to defaultModel.
func NewClaudeAnalyzer(apiKey, model string) *ClaudeAnalyzer {
	if model == "" {
		model = defaultModel
	}
	return &ClaudeAnalyzer{
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
}

func (c *ClaudeAnalyzer) Name() string { return c.model }

// Analyze sends incident evidence to Claude and parses the structured
// response. On any failure (network, parse, non-200) it returns an error —
// the caller is expected to fall back to NoopAnalyzer.
func (c *ClaudeAnalyzer) Analyze(ctx context.Context, e *Evidence) (*models.CausalAnalysis, error) {
	deterministicChain := BuildChain(e.Alerts, e.Dependencies)

	system := []contentBlock{
		{Type: "text", Text: systemPrompt, CacheControl: &cacheControl{Type: "ephemeral"}},
		{Type: "text", Text: renderDependencyGraph(e.Dependencies), CacheControl: &cacheControl{Type: "ephemeral"}},
	}

	userPayload := renderEvidence(e, deterministicChain)

	reqBody := messagesRequest{
		Model:     c.model,
		MaxTokens: defaultMaxTokens,
		System:    system,
		Messages: []message{
			{Role: "user", Content: userPayload},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var apiResp messagesResponse
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	text := extractText(apiResp)
	if text == "" {
		return nil, fmt.Errorf("empty model response")
	}

	parsed, err := parseModelJSON(text)
	if err != nil {
		return nil, fmt.Errorf("parse model JSON: %w", err)
	}

	// Merge: prefer the model's chain if non-empty, otherwise use the
	// deterministic one. The model is instructed to return links in the
	// same shape, but we defend against drift.
	chain := parsed.Chain
	if len(chain) == 0 {
		chain = deterministicChain
	}

	return &models.CausalAnalysis{
		Chain:      chain,
		Narrative:  parsed.Narrative,
		RootCause:  parsed.RootCause,
		Confidence: clamp01(parsed.Confidence),
		Model:      c.model,
		AnalyzedAt: time.Now().UTC(),
	}, nil
}

// ── Anthropic Messages API request/response shapes ─────────────────────────

type messagesRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    []contentBlock `json:"system,omitempty"`
	Messages  []message      `json:"messages"`
}

type contentBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
}

// ── Prompt construction ────────────────────────────────────────────────────

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

func extractText(r messagesResponse) string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// parseModelJSON extracts the first top-level JSON object from the model's
// response. Claude is instructed to emit pure JSON, but we tolerate a
// trailing/leading whitespace or stray markdown fence by extracting the
// substring between the first '{' and its matching '}'.
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
