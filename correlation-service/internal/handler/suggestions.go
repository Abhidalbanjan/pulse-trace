package handler

// Grounded suggested prompts (AI-SRE · E4).
//
// A blank chat box is a cold start — the user has to guess what to ask. This
// seeds the empty state with a few chips grounded in the tenant's live state
// (open incidents first), falling back to broadly-useful starters that each run
// a real backend query. The prompt selection is pure so its prioritization is
// unit-tested without a database.

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
)

const maxSuggestions = 5

// incidentLister is the slice of the incident repository this handler needs —
// narrowed to an interface so the handler is trivially testable with a fake.
type incidentLister interface {
	Query(ctx context.Context, params *models.IncidentQueryParams) (*repository.QueryResult, error)
}

// SuggestionsHandler serves grounded chat starter prompts.
type SuggestionsHandler struct {
	incidents incidentLister
}

func NewSuggestionsHandler(incidents incidentLister) *SuggestionsHandler {
	return &SuggestionsHandler{incidents: incidents}
}

func (h *SuggestionsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/chat/suggestions", h.GetSuggestions)
}

// starterPrompts are always-useful questions that each map onto a real tool the
// chat executor can run (error rate, latency, deploys, anomalies) — so even a
// tenant with no active incidents gets prompts that return grounded answers.
var starterPrompts = []string{
	"Which services have the highest error rate in the last hour?",
	"Show p99 latency for the slowest service right now",
	"Were there any deploys in the last 24 hours?",
	"Are there any anomalies across my services today?",
	"Give me a health summary of all services",
}

// buildSuggestions prioritizes prompts about the tenant's open incidents (the
// most likely thing an operator wants to ask about), then fills the remainder
// with grounded starters. Deduplicated, capped, incident-first. Pure.
func buildSuggestions(openIncidents []*models.Incident) []string {
	out := make([]string, 0, maxSuggestions)
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || seen[s] || len(out) >= maxSuggestions {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	// Incident-grounded prompts first, but leave room for at least two starters.
	for _, inc := range openIncidents {
		if inc == nil || inc.Status != models.IncidentStatusOpen {
			continue
		}
		if len(inc.ServiceNames) > 0 && inc.ServiceNames[0] != "" {
			add(fmt.Sprintf("What's the root cause of the active incident on %s?", inc.ServiceNames[0]))
		} else if inc.Title != "" {
			add(fmt.Sprintf("Summarize the active incident: %s", inc.Title))
		}
		if len(out) >= maxSuggestions-2 {
			break
		}
	}

	for _, s := range starterPrompts {
		add(s)
	}
	return out
}

// GetSuggestions returns starter chips for the chat empty state.
//
//	GET /api/v1/chat/suggestions
func (h *SuggestionsHandler) GetSuggestions(w http.ResponseWriter, r *http.Request) {
	var openIncidents []*models.Incident
	if h.incidents != nil {
		res, err := h.incidents.Query(r.Context(), &models.IncidentQueryParams{
			TenantID: tenantFromRequest(r),
			Status:   string(models.IncidentStatusOpen),
			PageSize: maxSuggestions,
		})
		if err != nil {
			// Live grounding is best-effort; fall back to the starters rather than
			// failing the whole request so the empty state always has chips.
			log.Printf("[SuggestionsHandler] incident query failed, using starters only: %v", err)
		} else if res != nil {
			openIncidents = res.Incidents
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"suggestions": buildSuggestions(openIncidents),
	})
}
