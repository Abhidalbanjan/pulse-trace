package causal

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pulsetrace/shared/models"
)

// stubAnalyzer is a scripted Analyzer: it returns errs[i] on call i, or
// succeeds once the script is exhausted (or when errs is nil).
type stubAnalyzer struct {
	name  string
	errs  []error
	calls int
}

func (s *stubAnalyzer) Name() string { return s.name }

func (s *stubAnalyzer) Analyze(ctx context.Context, _ *Evidence) (*models.CausalAnalysis, error) {
	s.calls++
	if s.calls <= len(s.errs) {
		if err := s.errs[s.calls-1]; err != nil {
			return nil, err
		}
	}
	return &models.CausalAnalysis{Model: s.name, RootCause: "from " + s.name}, nil
}

func alwaysFails(name string) *stubAnalyzer {
	errs := make([]error, 1000)
	for i := range errs {
		errs[i] = errors.New(name + " is down")
	}
	return &stubAnalyzer{name: name, errs: errs}
}

// newTestChain builds a chain with a controllable clock so cooldown behaviour
// is exercised without sleeping.
func newTestChain(t *testing.T, clock *time.Time, analyzers ...Analyzer) *FallbackAnalyzer {
	t.Helper()
	f, err := NewFallbackAnalyzer(analyzers...)
	if err != nil {
		t.Fatalf("NewFallbackAnalyzer: %v", err)
	}
	f.now = func() time.Time { return *clock }
	return f
}

func TestFallbackUsesPrimaryWhenHealthy(t *testing.T) {
	primary := &stubAnalyzer{name: "anthropic"}
	secondary := &stubAnalyzer{name: "openai"}
	now := time.Now()
	chain := newTestChain(t, &now, primary, secondary)

	got, err := chain.Analyze(context.Background(), &Evidence{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got.Model != "anthropic" {
		t.Errorf("Model = %q, want %q", got.Model, "anthropic")
	}
	if secondary.calls != 0 {
		t.Errorf("secondary was called %d times, want 0 — a healthy primary must not fan out", secondary.calls)
	}
}

func TestFallbackFailsOverToNextProvider(t *testing.T) {
	primary := &stubAnalyzer{name: "anthropic", errs: []error{errors.New("429 rate limited")}}
	secondary := &stubAnalyzer{name: "openai"}
	now := time.Now()
	chain := newTestChain(t, &now, primary, secondary)

	got, err := chain.Analyze(context.Background(), &Evidence{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got.Model != "openai" {
		t.Errorf("Model = %q, want %q — the chain should have fallen through", got.Model, "openai")
	}
	if primary.calls != 1 {
		t.Errorf("primary.calls = %d, want 1", primary.calls)
	}
}

func TestFallbackSkipsProviderDuringCooldown(t *testing.T) {
	primary := alwaysFails("anthropic")
	secondary := &stubAnalyzer{name: "openai"}
	now := time.Now()
	chain := newTestChain(t, &now, primary, secondary)

	if _, err := chain.Analyze(context.Background(), &Evidence{}); err != nil {
		t.Fatalf("first Analyze: %v", err)
	}
	if primary.calls != 1 {
		t.Fatalf("primary.calls after first attempt = %d, want 1", primary.calls)
	}

	// Still inside the cooldown window: the dead primary must not be dialed
	// again, since that would add its timeout to every incident.
	now = now.Add(defaultCooldown / 2)
	if _, err := chain.Analyze(context.Background(), &Evidence{}); err != nil {
		t.Fatalf("second Analyze: %v", err)
	}
	if primary.calls != 1 {
		t.Errorf("primary.calls during cooldown = %d, want 1 (should have been skipped)", primary.calls)
	}

	// Past the cooldown window: the primary is retried, because a provider is
	// never permanently evicted.
	now = now.Add(2 * defaultCooldown)
	if _, err := chain.Analyze(context.Background(), &Evidence{}); err != nil {
		t.Fatalf("third Analyze: %v", err)
	}
	if primary.calls != 2 {
		t.Errorf("primary.calls after cooldown expiry = %d, want 2 (should have been retried)", primary.calls)
	}
}

func TestFallbackSuccessResetsCooldown(t *testing.T) {
	// Fails once, then healthy — the cooldown must not persist after recovery.
	primary := &stubAnalyzer{name: "anthropic", errs: []error{errors.New("transient")}}
	secondary := &stubAnalyzer{name: "openai"}
	now := time.Now()
	chain := newTestChain(t, &now, primary, secondary)

	if _, err := chain.Analyze(context.Background(), &Evidence{}); err != nil {
		t.Fatalf("first Analyze: %v", err)
	}

	now = now.Add(defaultCooldown + time.Second)
	got, err := chain.Analyze(context.Background(), &Evidence{})
	if err != nil {
		t.Fatalf("second Analyze: %v", err)
	}
	if got.Model != "anthropic" {
		t.Fatalf("Model = %q, want anthropic after recovery", got.Model)
	}

	// Third call should go straight to the recovered primary with no cooldown
	// left over from the original failure.
	now = now.Add(time.Millisecond)
	got, err = chain.Analyze(context.Background(), &Evidence{})
	if err != nil {
		t.Fatalf("third Analyze: %v", err)
	}
	if got.Model != "anthropic" {
		t.Errorf("Model = %q, want anthropic — success should have cleared the cooldown", got.Model)
	}
}

func TestFallbackRetriesEveryoneWhenAllAreCoolingDown(t *testing.T) {
	// A stale-but-real analysis beats no analysis, so a fully-cooled-down
	// chain ignores its own cooldowns rather than failing fast.
	primary := &stubAnalyzer{name: "anthropic", errs: []error{errors.New("boom")}}
	secondary := &stubAnalyzer{name: "openai", errs: []error{errors.New("boom")}}
	now := time.Now()
	chain := newTestChain(t, &now, primary, secondary)

	if _, err := chain.Analyze(context.Background(), &Evidence{}); err == nil {
		t.Fatal("expected first Analyze to fail when every link errors")
	}

	// No clock advance: both links are cooling down.
	got, err := chain.Analyze(context.Background(), &Evidence{})
	if err != nil {
		t.Fatalf("second Analyze: %v", err)
	}
	if got.Model != "anthropic" {
		t.Errorf("Model = %q, want anthropic — all-cold chain should retry anyway", got.Model)
	}
}

func TestFallbackReturnsJoinedErrorWhenAllFail(t *testing.T) {
	primaryErr := errors.New("anthropic exploded")
	secondaryErr := errors.New("openai exploded")
	primary := &stubAnalyzer{name: "anthropic", errs: []error{primaryErr}}
	secondary := &stubAnalyzer{name: "openai", errs: []error{secondaryErr}}
	now := time.Now()
	chain := newTestChain(t, &now, primary, secondary)

	_, err := chain.Analyze(context.Background(), &Evidence{})
	if err == nil {
		t.Fatal("expected an error when every link fails")
	}
	if !errors.Is(err, primaryErr) || !errors.Is(err, secondaryErr) {
		t.Errorf("joined error lost a cause: %v", err)
	}
}

func TestFallbackAbortsOnCancelledContext(t *testing.T) {
	// A cancelled context would fail every remaining provider, so the chain
	// must stop rather than dial each one in turn.
	primary := &stubAnalyzer{name: "anthropic", errs: []error{errors.New("boom")}}
	secondary := &stubAnalyzer{name: "openai"}
	now := time.Now()
	chain := newTestChain(t, &now, primary, secondary)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := chain.Analyze(ctx, &Evidence{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
	if primary.calls != 0 || secondary.calls != 0 {
		t.Errorf("calls = (%d, %d), want (0, 0) — no provider should be dialed on a dead context",
			primary.calls, secondary.calls)
	}
}

func TestNewFallbackAnalyzerRejectsEmptyChain(t *testing.T) {
	if _, err := NewFallbackAnalyzer(); err == nil {
		t.Error("expected an error for an empty chain")
	}
	if _, err := NewFallbackAnalyzer(nil, nil); err == nil {
		t.Error("expected an error when every analyzer is nil")
	}
}

func TestFallbackName(t *testing.T) {
	now := time.Now()
	chain := newTestChain(t, &now, &stubAnalyzer{name: "claude"}, &stubAnalyzer{name: "gpt-4o"})
	if got, want := chain.Name(), "fallback[claude→gpt-4o]"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestParseProviderChain(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []ProviderSpec
	}{
		{
			name: "provider and model pairs",
			in:   "anthropic:claude-sonnet-4-5,openai:gpt-4o-mini,ollama:llama3",
			want: []ProviderSpec{
				{Provider: "anthropic", Model: "claude-sonnet-4-5"},
				{Provider: "openai", Model: "gpt-4o-mini"},
				{Provider: "ollama", Model: "llama3"},
			},
		},
		{
			name: "model is optional",
			in:   "anthropic,ollama:llama3",
			want: []ProviderSpec{
				{Provider: "anthropic"},
				{Provider: "ollama", Model: "llama3"},
			},
		},
		{
			name: "whitespace and empty segments tolerated",
			in:   " anthropic : claude-3 , , openai ,",
			want: []ProviderSpec{
				{Provider: "anthropic", Model: "claude-3"},
				{Provider: "openai"},
			},
		},
		{
			name: "provider names are lowercased",
			in:   "Anthropic,OpenAI",
			want: []ProviderSpec{{Provider: "anthropic"}, {Provider: "openai"}},
		},
		{name: "empty input", in: "", want: nil},
		{name: "only separators", in: " , , ", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseProviderChain(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseProviderChain(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewAnalyzerChainRejectsUnknownProviders(t *testing.T) {
	// Every spec fails to construct, so the caller gets an error and can pick
	// its own last resort rather than receiving a chain that fails per-incident.
	_, err := NewAnalyzerChain([]ProviderSpec{{Provider: "not-a-real-provider"}})
	if err == nil {
		t.Fatal("expected an error when no provider can be initialized")
	}
}

func TestNewAnalyzerChainRejectsEmptySpecs(t *testing.T) {
	if _, err := NewAnalyzerChain(nil); err == nil {
		t.Error("expected an error for an empty spec list")
	}
}

func TestFallbackHealthReportsPerLinkState(t *testing.T) {
	clock := time.Now()
	primary := alwaysFails("primary")
	backup := &stubAnalyzer{name: "backup"}
	f := newTestChain(t, &clock, primary, backup)

	// Both links start healthy with no failures.
	h := f.Health()
	if len(h) != 2 {
		t.Fatalf("expected 2 links, got %d", len(h))
	}
	if !h[0].Healthy || !h[1].Healthy || h[0].Failures != 0 {
		t.Fatalf("expected both links healthy and unfailed, got %+v", h)
	}

	// One analysis: primary fails (goes into cooldown), backup answers.
	if _, err := f.Analyze(context.Background(), &Evidence{}); err != nil {
		t.Fatalf("expected success via backup, got %v", err)
	}

	h = f.Health()
	if h[0].Healthy {
		t.Error("primary should be unhealthy (cooling down) after a failure")
	}
	if h[0].Failures != 1 {
		t.Errorf("expected 1 failure on primary, got %d", h[0].Failures)
	}
	if h[0].CooldownRemaining == "" {
		t.Error("expected a cooldown-remaining string on the unhealthy link")
	}
	if !h[1].Healthy {
		t.Error("backup should remain healthy after answering")
	}

	// FallbackAnalyzer satisfies HealthReporter (the handler type-asserts for it).
	var _ HealthReporter = f
}
