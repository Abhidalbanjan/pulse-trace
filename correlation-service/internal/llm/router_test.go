package llm

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// stubProvider is a scripted Provider: it returns errs[i] on call i, then
// succeeds once the script is exhausted.
type stubProvider struct {
	name  string
	errs  []error
	calls int
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Chat(_ context.Context, _ []Message) (Response, error) {
	s.calls++
	if s.calls <= len(s.errs) {
		if err := s.errs[s.calls-1]; err != nil {
			return Response{}, err
		}
	}
	return Response{Text: "answered by " + s.name}, nil
}

func newTestChain(t *testing.T, clock *time.Time, providers ...Provider) *FallbackProvider {
	t.Helper()
	f, err := NewFallbackProvider(providers...)
	if err != nil {
		t.Fatalf("NewFallbackProvider: %v", err)
	}
	f.now = func() time.Time { return *clock }
	return f
}

func TestChainUsesPrimaryWhenHealthy(t *testing.T) {
	primary := &stubProvider{name: "anthropic"}
	secondary := &stubProvider{name: "ollama"}
	now := time.Now()
	chain := newTestChain(t, &now, primary, secondary)

	got, err := chain.Chat(context.Background(), nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got.Text != "answered by anthropic" {
		t.Errorf("Text = %q, want the primary's answer", got.Text)
	}
	if secondary.calls != 0 {
		t.Errorf("secondary.calls = %d, want 0 — a healthy primary must not fan out", secondary.calls)
	}
}

func TestChainFailsOverToNextProvider(t *testing.T) {
	primary := &stubProvider{name: "anthropic", errs: []error{errors.New("429 rate limited")}}
	secondary := &stubProvider{name: "ollama"}
	now := time.Now()
	chain := newTestChain(t, &now, primary, secondary)

	got, err := chain.Chat(context.Background(), nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got.Text != "answered by ollama" {
		t.Errorf("Text = %q, want the fallback's answer", got.Text)
	}
}

func TestChainSkipsProviderDuringCooldown(t *testing.T) {
	dead := &stubProvider{name: "anthropic", errs: repeatErr("down", 100)}
	backup := &stubProvider{name: "ollama"}
	now := time.Now()
	chain := newTestChain(t, &now, dead, backup)

	if _, err := chain.Chat(context.Background(), nil); err != nil {
		t.Fatalf("first Chat: %v", err)
	}
	if dead.calls != 1 {
		t.Fatalf("dead.calls = %d, want 1", dead.calls)
	}

	// Inside the cooldown window the dead provider must not be dialed again;
	// otherwise every chat message pays its timeout.
	now = now.Add(defaultProviderCooldown / 2)
	if _, err := chain.Chat(context.Background(), nil); err != nil {
		t.Fatalf("second Chat: %v", err)
	}
	if dead.calls != 1 {
		t.Errorf("dead.calls during cooldown = %d, want 1 (should have been skipped)", dead.calls)
	}

	now = now.Add(2 * defaultProviderCooldown)
	if _, err := chain.Chat(context.Background(), nil); err != nil {
		t.Fatalf("third Chat: %v", err)
	}
	if dead.calls != 2 {
		t.Errorf("dead.calls after cooldown = %d, want 2 (should have been retried)", dead.calls)
	}
}

func TestChainSuccessResetsCooldown(t *testing.T) {
	primary := &stubProvider{name: "anthropic", errs: []error{errors.New("transient")}}
	backup := &stubProvider{name: "ollama"}
	now := time.Now()
	chain := newTestChain(t, &now, primary, backup)

	if _, err := chain.Chat(context.Background(), nil); err != nil {
		t.Fatalf("first Chat: %v", err)
	}

	now = now.Add(defaultProviderCooldown + time.Second)
	if _, err := chain.Chat(context.Background(), nil); err != nil {
		t.Fatalf("second Chat: %v", err)
	}

	// Immediately again: the recovered primary must be live, not still cooling.
	now = now.Add(time.Millisecond)
	got, err := chain.Chat(context.Background(), nil)
	if err != nil {
		t.Fatalf("third Chat: %v", err)
	}
	if got.Text != "answered by anthropic" {
		t.Errorf("Text = %q, want the recovered primary's answer", got.Text)
	}
}

func TestChainRetriesEveryoneWhenAllAreCoolingDown(t *testing.T) {
	primary := &stubProvider{name: "anthropic", errs: []error{errors.New("boom")}}
	backup := &stubProvider{name: "ollama", errs: []error{errors.New("boom")}}
	now := time.Now()
	chain := newTestChain(t, &now, primary, backup)

	if _, err := chain.Chat(context.Background(), nil); err == nil {
		t.Fatal("expected an error when every provider fails")
	}

	// No clock advance — both links are cooling down, but the chain should
	// still try rather than guarantee the user an error.
	got, err := chain.Chat(context.Background(), nil)
	if err != nil {
		t.Fatalf("second Chat: %v", err)
	}
	if got.Text != "answered by anthropic" {
		t.Errorf("Text = %q, want the retried primary's answer", got.Text)
	}
}

func TestChainReturnsJoinedErrorWhenAllFail(t *testing.T) {
	primaryErr := errors.New("anthropic exploded")
	backupErr := errors.New("ollama exploded")
	now := time.Now()
	chain := newTestChain(t, &now,
		&stubProvider{name: "anthropic", errs: []error{primaryErr}},
		&stubProvider{name: "ollama", errs: []error{backupErr}},
	)

	_, err := chain.Chat(context.Background(), nil)
	if err == nil {
		t.Fatal("expected an error when every provider fails")
	}
	if !errors.Is(err, primaryErr) || !errors.Is(err, backupErr) {
		t.Errorf("joined error lost a cause: %v", err)
	}
}

func TestChainAbortsOnCancelledContext(t *testing.T) {
	primary := &stubProvider{name: "anthropic", errs: []error{errors.New("boom")}}
	backup := &stubProvider{name: "ollama"}
	now := time.Now()
	chain := newTestChain(t, &now, primary, backup)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := chain.Chat(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
	if primary.calls != 0 || backup.calls != 0 {
		t.Errorf("calls = (%d, %d), want (0, 0) — no provider should be dialed on a dead context",
			primary.calls, backup.calls)
	}
}

func TestNewFallbackProviderRejectsEmptyChain(t *testing.T) {
	if _, err := NewFallbackProvider(); err == nil {
		t.Error("expected an error for an empty chain")
	}
	if _, err := NewFallbackProvider(nil, nil); err == nil {
		t.Error("expected an error when every provider is nil")
	}
}

func TestChainName(t *testing.T) {
	now := time.Now()
	chain := newTestChain(t, &now, &stubProvider{name: "anthropic/claude"}, &stubProvider{name: "ollama/llama3"})
	if got, want := chain.Name(), "fallback[anthropic/claude→ollama/llama3]"; got != want {
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
			in:   "anthropic:claude-sonnet-4-5,openai:gpt-4o-mini,ollama:llama3",
			name: "provider and model pairs",
			want: []ProviderSpec{
				{Provider: "anthropic", Model: "claude-sonnet-4-5"},
				{Provider: "openai", Model: "gpt-4o-mini"},
				{Provider: "ollama", Model: "llama3"},
			},
		},
		{
			name: "model is optional",
			in:   "anthropic,ollama:llama3",
			want: []ProviderSpec{{Provider: "anthropic"}, {Provider: "ollama", Model: "llama3"}},
		},
		{
			name: "whitespace and empty segments tolerated",
			in:   " anthropic : claude-3 , , openai ,",
			want: []ProviderSpec{{Provider: "anthropic", Model: "claude-3"}, {Provider: "openai"}},
		},
		{name: "empty input", in: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseProviderChain(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseProviderChain(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewProviderChainRejectsUnknownProvider(t *testing.T) {
	if _, err := NewProviderChain([]ProviderSpec{{Provider: "not-a-real-provider"}}); err == nil {
		t.Error("expected an error when no provider can be initialized")
	}
}

// ── Protocol parsing (shared by every provider) ────────────────────────────

func TestParseResponseExtractsQuery(t *testing.T) {
	got := ParseResponse("<query>\n{\"tool\": \"search_logs\", \"args\": {\"service\": \"checkout\"}}\n</query>")
	if got.Query == nil {
		t.Fatal("Query = nil, want a parsed query request")
	}
	if got.Query.Tool != "search_logs" {
		t.Errorf("Tool = %q, want search_logs", got.Query.Tool)
	}
	if got.Query.Args["service"] != "checkout" {
		t.Errorf("Args[service] = %q, want checkout", got.Query.Args["service"])
	}
	if got.Text != "" {
		t.Errorf("Text = %q, want empty — a query request carries no answer", got.Text)
	}
}

func TestParseResponseExtractsActionAndStripsItFromText(t *testing.T) {
	raw := "I'd roll back checkout-service.\n<action>\n{\"title\":\"Execute Rollback\",\"type\":\"ROLLBACK\",\"target\":\"checkout-service\"}\n</action>"
	got := ParseResponse(raw)
	if got.Action == nil {
		t.Fatal("Action = nil, want a parsed action card")
	}
	if got.Action.Type != "ROLLBACK" || got.Action.Target != "checkout-service" {
		t.Errorf("Action = %+v, want a ROLLBACK targeting checkout-service", got.Action)
	}
	if got.Text != "I'd roll back checkout-service." {
		t.Errorf("Text = %q, want the action block stripped", got.Text)
	}
}

func TestParseResponseDegradesToPlainTextOnMalformedBlocks(t *testing.T) {
	// A model that emits an unparseable block should produce a chatty answer,
	// not a failed request.
	raw := "here you go <action>{not json}</action>"
	got := ParseResponse(raw)
	if got.Action != nil {
		t.Errorf("Action = %+v, want nil for a malformed block", got.Action)
	}
	if got.Text != raw {
		t.Errorf("Text = %q, want the raw content preserved", got.Text)
	}
}

func TestParseResponsePlainAnswer(t *testing.T) {
	got := ParseResponse("p99 latency is elevated on checkout-service.")
	if got.Query != nil || got.Action != nil {
		t.Errorf("got Query=%v Action=%v, want both nil for a plain answer", got.Query, got.Action)
	}
	if got.Text != "p99 latency is elevated on checkout-service." {
		t.Errorf("Text = %q", got.Text)
	}
}

func repeatErr(msg string, n int) []error {
	errs := make([]error, n)
	for i := range errs {
		errs[i] = errors.New(msg)
	}
	return errs
}
