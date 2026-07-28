package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// See FallbackProvider for why a cooldown exists at all.
const (
	defaultProviderCooldown = 30 * time.Second
	maxCooldownShift        = 5 // 30s → 16m ceiling
)

// FallbackProvider chains several Providers and returns the first successful
// completion, in the order the providers were given.
//
// It mirrors shared/causal.FallbackAnalyzer deliberately: the two surfaces
// (incident RCA and interactive chat/SLO) have different types but the same
// operational requirement — a rate limit or outage at the primary model must
// degrade the feature, not remove it.
//
// A failed provider is skipped for a cooldown window that doubles on each
// consecutive failure. Without that, a hard-down provider (expired key,
// regional outage) would be dialed and time out on every single chat message
// before the chain moved on. No provider is ever permanently evicted: if all
// of them are cooling down, the cooldown is ignored for that attempt.
//
// Safe for concurrent use.
type FallbackProvider struct {
	links    []*providerLink
	cooldown time.Duration

	// now is injectable so cooldown behaviour is testable without sleeping.
	now func() time.Time

	mu sync.Mutex
}

type providerLink struct {
	provider  Provider
	downUntil time.Time
	failures  int
}

// NewFallbackProvider chains the given providers in priority order. Nil
// entries are dropped. Returns an error if no usable provider remains.
func NewFallbackProvider(providers ...Provider) (*FallbackProvider, error) {
	links := make([]*providerLink, 0, len(providers))
	for _, p := range providers {
		if p == nil {
			continue
		}
		links = append(links, &providerLink{provider: p})
	}
	if len(links) == 0 {
		return nil, errors.New("llm: fallback chain requires at least one provider")
	}
	return &FallbackProvider{
		links:    links,
		cooldown: defaultProviderCooldown,
		now:      time.Now,
	}, nil
}

// Name describes the chain, e.g. "fallback[anthropic/claude-sonnet-4-5→ollama/llama3]".
func (f *FallbackProvider) Name() string {
	names := make([]string, 0, len(f.links))
	for _, l := range f.links {
		names = append(names, l.provider.Name())
	}
	return "fallback[" + strings.Join(names, "→") + "]"
}

// Chat tries each link in order and returns the first success.
//
// Context cancellation short-circuits the chain — a caller whose deadline has
// passed gains nothing from dialing the next provider, and a cancelled
// context would fail every remaining link anyway.
func (f *FallbackProvider) Chat(ctx context.Context, messages []Message) (Response, error) {
	candidates := f.candidates()

	var errs []error
	for _, l := range candidates {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("chain aborted: %w", err))
			return Response{}, errors.Join(errs...)
		}

		resp, err := l.provider.Chat(ctx, messages)
		if err == nil {
			f.recordSuccess(l)
			return resp, nil
		}

		cooldown := f.recordFailure(l)
		log.Printf("llm: provider %q failed (%v) — skipping it for %s and trying the next provider",
			l.provider.Name(), err, cooldown)
		errs = append(errs, fmt.Errorf("%s: %w", l.provider.Name(), err))
	}

	return Response{}, fmt.Errorf("all %d LLM providers failed: %w", len(candidates), errors.Join(errs...))
}

// candidates returns the links to try, in order, skipping any in cooldown. If
// that leaves nothing, every link is returned — a likely-failing attempt
// still beats guaranteeing the user an error.
func (f *FallbackProvider) candidates() []*providerLink {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := f.now()
	live := make([]*providerLink, 0, len(f.links))
	for _, l := range f.links {
		if now.Before(l.downUntil) {
			continue
		}
		live = append(live, l)
	}
	if len(live) == 0 {
		live = append(live, f.links...)
	}
	return live
}

func (f *FallbackProvider) recordSuccess(l *providerLink) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l.failures = 0
	l.downUntil = time.Time{}
}

// recordFailure applies exponential backoff and returns the applied cooldown.
func (f *FallbackProvider) recordFailure(l *providerLink) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()

	l.failures++
	shift := l.failures - 1
	if shift > maxCooldownShift {
		shift = maxCooldownShift
	}
	cooldown := f.cooldown << shift
	l.downUntil = f.now().Add(cooldown)
	return cooldown
}

// ── Chain construction from configuration ──────────────────────────────────

// ProviderSpec is one link in a configured chain: a backend name and,
// optionally, the model to use with it.
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
// The model half is optional. Order is preserved and is the failover order.
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

// NewProviderChain builds a FallbackProvider from the given specs.
//
// Links that fail to construct are skipped with a warning rather than failing
// the whole chain — a deployment configured for "anthropic,openai,ollama" but
// holding only an Anthropic key should run with what it has. Returns an error
// only if nothing could be constructed, so the caller can pick its own last
// resort.
func NewProviderChain(specs []ProviderSpec) (Provider, error) {
	if len(specs) == 0 {
		return nil, errors.New("llm: no providers configured")
	}

	providers := make([]Provider, 0, len(specs))
	var errs []error
	for _, spec := range specs {
		p, err := NewLangChainProvider(spec.Provider, spec.Model)
		if err != nil {
			log.Printf("llm: skipping provider %q — %v", spec, err)
			errs = append(errs, fmt.Errorf("%s: %w", spec, err))
			continue
		}
		providers = append(providers, p)
	}

	if len(providers) == 0 {
		return nil, fmt.Errorf("llm: no provider could be initialized: %w", errors.Join(errs...))
	}

	chain, err := NewFallbackProvider(providers...)
	if err != nil {
		return nil, err
	}
	return chain, nil
}
