package handler

import (
	"net/http"

	"github.com/pulsetrace/shared/causal"
	"github.com/pulsetrace/shared/models"
)

// CausalHealthHandler surfaces the state of the causal-AI provider chain so an
// operator can see whether the flagship RCA feature is actually working and on
// which provider — without having to trigger an incident to find out.
//
// It reads live state from the configured analyzer. The rule-based
// (Noop) analyzer has no providers to be up or down, so the endpoint reports
// that explicitly rather than pretending a provider chain exists.
type CausalHealthHandler struct {
	analyzer causal.Analyzer
}

func NewCausalHealthHandler(analyzer causal.Analyzer) *CausalHealthHandler {
	return &CausalHealthHandler{analyzer: analyzer}
}

func (h *CausalHealthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/causal/providers", h.Providers)
}

// causalProvidersResponse is the shape returned to the UI badge.
type causalProvidersResponse struct {
	// Analyzer is the configured chain descriptor, e.g.
	// "fallback[claude-sonnet-4-5→gpt-4o→llama3]" or "rule-based".
	Analyzer string `json:"analyzer"`
	// LLMEnabled is false when the deterministic rule-based analyzer is the
	// only path (no API keys / CAUSAL_DISABLED) — the UI shows "rule-based"
	// rather than a health list in that case.
	LLMEnabled bool `json:"llm_enabled"`
	// Providers is the per-link health, in failover order. Empty for rule-based.
	Providers []causal.ProviderHealth `json:"providers"`
}

// Providers reports the causal analyzer chain's health.
//
// Health is a service-wide property of how the deployment is configured, not a
// per-tenant one, so this endpoint is not tenant-scoped. It exposes only
// provider identifiers and up/down state — never keys or request contents.
func (h *CausalHealthHandler) Providers(w http.ResponseWriter, r *http.Request) {
	resp := causalProvidersResponse{
		Analyzer:  h.analyzer.Name(),
		Providers: []causal.ProviderHealth{},
	}
	if reporter, ok := h.analyzer.(causal.HealthReporter); ok {
		resp.LLMEnabled = true
		resp.Providers = reporter.Health()
	}
	writeJSON(w, http.StatusOK, models.OK(resp))
}
