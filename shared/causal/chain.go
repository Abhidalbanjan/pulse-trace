package causal

import (
	"errors"
	"fmt"
	"log"
	"strings"
)

// ProviderSpec is one link in a configured analyzer chain: a LangChain
// provider name and, optionally, the model to use with it.
type ProviderSpec struct {
	Provider string
	Model    string
}

func (s ProviderSpec) String() string {
	if s.Model == "" {
		return s.Provider
	}
	return s.Provider + ":" + s.Model
}

// ParseProviderChain parses the LLM_PROVIDERS format:
//
//	anthropic:claude-sonnet-4-5,openai:gpt-4o-mini,ollama:llama3
//
// The model half is optional ("anthropic" alone uses the provider's default
// model). Order is preserved and is the failover order. Empty segments and
// surrounding whitespace are tolerated so the value can be written across
// lines in a compose file or Helm values.yaml.
func ParseProviderChain(spec string) []ProviderSpec {
	var out []ProviderSpec
	for _, segment := range strings.Split(spec, ",") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		provider, model, _ := strings.Cut(segment, ":")
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		out = append(out, ProviderSpec{Provider: provider, Model: strings.TrimSpace(model)})
	}
	return out
}

// NewAnalyzerChain builds a FallbackAnalyzer from the given specs.
//
// Links that fail to construct are skipped with a warning rather than failing
// the whole chain. That is the common case in practice, not an edge case: a
// deployment configured with "anthropic,openai,ollama" but holding only an
// Anthropic key should run with Anthropic and Ollama, not refuse to start.
// langchaingo's constructors validate credentials at construction time, so a
// missing API key surfaces here rather than on the first incident.
//
// Returns an error only if no link could be constructed, letting the caller
// choose its own last resort (NoopAnalyzer).
func NewAnalyzerChain(specs []ProviderSpec) (Analyzer, error) {
	if len(specs) == 0 {
		return nil, errors.New("causal: no LLM providers configured")
	}

	analyzers := make([]Analyzer, 0, len(specs))
	var errs []error
	for _, spec := range specs {
		a, err := NewLangChainAnalyzer(spec.Provider, spec.Model)
		if err != nil {
			log.Printf("causal: skipping provider %q — %v", spec, err)
			errs = append(errs, fmt.Errorf("%s: %w", spec, err))
			continue
		}
		analyzers = append(analyzers, a)
	}

	if len(analyzers) == 0 {
		return nil, fmt.Errorf("causal: no LLM provider could be initialized: %w", errors.Join(errs...))
	}

	// A one-link chain still goes through FallbackAnalyzer rather than being
	// unwrapped. The cooldown bookkeeping is harmless with one link, and
	// keeping the type uniform means the caller's logging and the
	// Analyzer.Name() output don't change shape depending on how many
	// providers happened to initialize.
	chain, err := NewFallbackAnalyzer(analyzers...)
	if err != nil {
		return nil, err
	}
	return chain, nil
}
