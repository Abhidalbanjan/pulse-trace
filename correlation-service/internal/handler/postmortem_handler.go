package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pulsetrace/correlation-service/internal/engine"
	"github.com/pulsetrace/correlation-service/internal/llm"
	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
)

// postmortemGenTimeout bounds how long a single LLM draft may take before the
// handler falls back to the deterministic template — a slow provider must never
// hang the request.
const postmortemGenTimeout = 45 * time.Second

// PostmortemHandler serves the AI-drafted, editable incident postmortem
// (Incidents · E1): generate from evidence, read, and save edits. Generation
// degrades to a deterministic template when no LLM provider is configured or a
// draft fails, so the feature never hard-fails.
type PostmortemHandler struct {
	incidents *repository.IncidentRepository
	pms       *repository.PostmortemRepository
	provider  llm.Provider // may be a rule-based/Noop provider; generation still works via fallback
}

func NewPostmortemHandler(incidents *repository.IncidentRepository, pms *repository.PostmortemRepository, provider llm.Provider) *PostmortemHandler {
	return &PostmortemHandler{incidents: incidents, pms: pms, provider: provider}
}

func (h *PostmortemHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/incidents/{id}/postmortem", h.Get)
	mux.HandleFunc("POST /api/v1/incidents/{id}/postmortem", h.Generate)
	mux.HandleFunc("PUT /api/v1/incidents/{id}/postmortem", h.SaveEdit)
}

// Get returns the stored postmortem, or {data:null} when none exists yet (so the
// UI can offer to generate one rather than treating "none" as an error).
func (h *PostmortemHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenant, id, ok := h.scope(w, r)
	if !ok {
		return
	}
	pm, err := h.pms.Get(r.Context(), tenant, id)
	if errors.Is(err, repository.ErrPostmortemNotFound) {
		writeJSON(w, http.StatusOK, models.OK(nil))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to load postmortem"))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(pm))
}

// Generate drafts a postmortem from the incident's evidence and stores it,
// replacing any prior draft. Uses the LLM when available, else a deterministic
// template.
func (h *PostmortemHandler) Generate(w http.ResponseWriter, r *http.Request) {
	tenant, id, ok := h.scope(w, r)
	if !ok {
		return
	}

	inc, err := h.incidents.GetByIDForTenant(r.Context(), tenant, id)
	if err != nil || inc == nil {
		writeJSON(w, http.StatusNotFound, models.Fail("incident not found"))
		return
	}
	// Alerts + timeline are best-effort context; a failure to load either still
	// yields a valid (if sparser) postmortem rather than a hard error.
	alerts, err := h.incidents.AlertsForIncident(r.Context(), id)
	if err != nil {
		log.Printf("postmortem: load alerts for %s: %v", id, err)
	}
	timeline, err := h.incidents.Timeline(r.Context(), id)
	if err != nil {
		log.Printf("postmortem: load timeline for %s: %v", id, err)
	}

	content, model := h.draft(r.Context(), inc, alerts, timeline)

	pm, err := h.pms.Upsert(r.Context(), tenant, id, content, model)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to store postmortem"))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(pm))
}

// draft returns the postmortem content + the model that produced it. It tries
// the LLM first and falls back to the deterministic template on any failure or
// empty response.
func (h *PostmortemHandler) draft(ctx context.Context, inc *models.Incident, alerts []models.IncidentAlert, timeline []models.IncidentTimelineEvent) (content, model string) {
	fallback := func() (string, string) {
		return engine.DeterministicPostmortem(inc, alerts, timeline), "template"
	}
	if h.provider == nil {
		return fallback()
	}

	system, user := engine.BuildPostmortemPrompt(inc, alerts, timeline)
	genCtx, cancel := context.WithTimeout(ctx, postmortemGenTimeout)
	defer cancel()

	resp, err := h.provider.Chat(genCtx, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
	if err != nil || strings.TrimSpace(resp.Text) == "" {
		if err != nil {
			log.Printf("postmortem: LLM draft failed for %s, using template: %v", inc.ID, err)
		}
		return fallback()
	}
	return strings.TrimSpace(resp.Text), h.provider.Name()
}

// SaveEdit persists a human edit to an existing postmortem.
func (h *PostmortemHandler) SaveEdit(w http.ResponseWriter, r *http.Request) {
	tenant, id, ok := h.scope(w, r)
	if !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, models.Fail("invalid request body"))
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("content is required"))
		return
	}
	pm, err := h.pms.SaveEdit(r.Context(), tenant, id, body.Content)
	if errors.Is(err, repository.ErrPostmortemNotFound) {
		writeJSON(w, http.StatusNotFound, models.Fail("no postmortem to edit — generate one first"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to save postmortem"))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(pm))
}

// scope resolves and validates the tenant + incident id, failing closed on an
// empty tenant (never operate cross-tenant) or missing id.
func (h *PostmortemHandler) scope(w http.ResponseWriter, r *http.Request) (tenant, id string, ok bool) {
	tenant = tenantFromRequest(r)
	if tenant == "" {
		writeJSON(w, http.StatusUnauthorized, models.Fail("unauthenticated"))
		return "", "", false
	}
	id = strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("incident id is required"))
		return "", "", false
	}
	return tenant, id, true
}
