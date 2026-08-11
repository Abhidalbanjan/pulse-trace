package causal

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/pulsetrace/shared/models"
)

// defaultCooldown is how long a provider is skipped after its first failure.
// It doubles on each consecutive failure up to maxCooldownShift doublings.
//
// The point of the cooldown is latency, not correctness: without it, a
// provider whose API is hard-down (expired key, regional outage) would be
// dialed and time out on *every* incident before the chain moved on, adding
// its full timeout to the critical path of RCA for as long as the outage
// lasts. With it, the chain pays that cost once and then routes around the
// dead provider until the cooldown expires.
const (
	defaultCooldown  = 30 * time.Second
	maxCooldownShift = 5 // 30s → 16m ceiling
)

// FallbackAnalyzer chains several Analyzers and returns the first successful
// result, in the order the analyzers were given.
//
// This is what makes multi-provider causal AI real rather than nominal:
// NewLangChainAnalyzer can build a client for Anthropic, OpenAI, GoogleAI, or
// Ollama, but a single one of those is a single point of failure for the
// flagship feature. A chain of "anthropic → openai → ollama" degrades
// gracefully — a rate limit or an outage at the primary costs one failed
// request, not a lost root-cause analysis.
//
// Failures are per-link and sticky for a cooldown window (see defaultCooldown).
// A link is never permanently removed: if every link is cooling down, the
// cooldown is ignored for that attempt and all links are retried, because a
// stale analysis is worth more than no analysis.
//
// Safe for concurrent use.
type FallbackAnalyzer struct {
	links    []*fallbackLink
	cooldown time.Duration

	// now is injectable so cooldown behaviour is testable without sleeping.
	now func() time.Time

	mu sync.Mutex
}

type fallbackLink struct {
	analyzer  Analyzer
	downUntil time.Time
	failures  int
}

// NewFallbackAnalyzer chains the given analyzers in priority order. Nil
// entries are dropped. It returns an error if no usable analyzer remains, so
// callers can fall back to NoopAnalyzer explicitly rather than silently
// getting an empty chain that fails on every incident.
func NewFallbackAnalyzer(analyzers ...Analyzer) (*FallbackAnalyzer, error) {
	links := make([]*fallbackLink, 0, len(analyzers))
	for _, a := range analyzers {
		if a == nil {
			continue
		}
		links = append(links, &fallbackLink{analyzer: a})
	}
	if len(links) == 0 {
		return nil, errors.New("causal: fallback chain requires at least one analyzer")
	}
	return &FallbackAnalyzer{
		links:    links,
		cooldown: defaultCooldown,
		now:      time.Now,
	}, nil
}

// Name describes the chain, e.g. "fallback[claude-sonnet-4-5→gpt-4o→llama3]".
//
// The per-incident record of which provider actually answered lives on
// models.CausalAnalysis.Model, which each link sets from its own Name() — so
// this method describes configuration, not attribution.
func (f *FallbackAnalyzer) Name() string {
	names := make([]string, 0, len(f.links))
	for _, l := range f.links {
		names = append(names, l.analyzer.Name())
	}
	return "fallback[" + strings.Join(names, "→") + "]"
}

// Analyze tries each link in order and returns the first success.
//
// Context cancellation short-circuits the whole chain: if the caller's
// deadline has passed there is no point dialing the next provider, and a
// cancelled context would fail every remaining link anyway. That check is
// deliberately made *before* each attempt, so a context that expires mid-chain
// stops the chain rather than burning through the remaining providers.
func (f *FallbackAnalyzer) Analyze(ctx context.Context, e *Evidence) (*models.CausalAnalysis, error) {
	candidates := f.candidates()

	var errs []error
	for _, l := range candidates {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("chain aborted: %w", err))
			return nil, errors.Join(errs...)
		}

		result, err := l.analyzer.Analyze(ctx, e)
		if err == nil {
			f.recordSuccess(l)
			return result, nil
		}

		cooldown := f.recordFailure(l)
		log.Printf("causal: analyzer %q failed (%v) — skipping it for %s and trying the next provider",
			l.analyzer.Name(), err, cooldown)
		errs = append(errs, fmt.Errorf("%s: %w", l.analyzer.Name(), err))
	}

	return nil, fmt.Errorf("all %d causal analyzers failed: %w", len(candidates), errors.Join(errs...))
}

// candidates returns the links to try, in order, skipping any that are in
// their cooldown window. If that leaves nothing, every link is returned —
// preferring a likely-failing attempt over guaranteed no analysis.
func (f *FallbackAnalyzer) candidates() []*fallbackLink {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := f.now()
	live := make([]*fallbackLink, 0, len(f.links))
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

// ProviderHealth is a point-in-time snapshot of one link in the analyzer
// chain, suitable for surfacing to an operator. It answers "is my flagship
// causal AI actually working, and on which provider?" without exposing the
// internal link type or requiring a live request.
type ProviderHealth struct {
	Name              string `json:"name"`                 // analyzer identifier, e.g. "claude-sonnet-4-5"
	Healthy           bool   `json:"healthy"`              // not currently in a cooldown window
	Failures          int    `json:"failures"`             // consecutive failures (0 once it recovers)
	CooldownRemaining string `json:"cooldown_remaining,omitempty"` // human-readable, only while unhealthy
}

// HealthReporter is implemented by analyzers that can report per-provider
// health. FallbackAnalyzer satisfies it; NoopAnalyzer does not (it has no
// providers to be up or down). The correlation-service health endpoint type-
// asserts for this so it works with either analyzer.
type HealthReporter interface {
	Health() []ProviderHealth
}

// Health returns the current state of every link in the chain, in priority
// order. Safe for concurrent use.
func (f *FallbackAnalyzer) Health() []ProviderHealth {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := f.now()
	out := make([]ProviderHealth, 0, len(f.links))
	for _, l := range f.links {
		h := ProviderHealth{
			Name:     l.analyzer.Name(),
			Failures: l.failures,
			Healthy:  !now.Before(l.downUntil),
		}
		if !h.Healthy {
			h.CooldownRemaining = l.downUntil.Sub(now).Round(time.Second).String()
		}
		out = append(out, h)
	}
	return out
}

func (f *FallbackAnalyzer) recordSuccess(l *fallbackLink) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l.failures = 0
	l.downUntil = time.Time{}
}

// recordFailure applies exponential backoff to the link and returns the
// cooldown that was applied, for logging.
func (f *FallbackAnalyzer) recordFailure(l *fallbackLink) time.Duration {
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
