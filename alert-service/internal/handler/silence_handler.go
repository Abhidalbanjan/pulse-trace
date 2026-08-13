package handler

// Alert silences / maintenance windows (Alerts · E2).
//
// A silence suppresses alerts matching a matcher (service / level / message
// substring) during an active window, so a known-noisy deploy or planned
// maintenance doesn't flood the stream or page. Whether a given alert is
// silenced is a pure function of the silence set and the clock, so it's
// unit-tested directly; the handler layer just does CRUD + annotation.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/pulsetrace/alert-service/internal/repository"
	"github.com/pulsetrace/shared/models"
)

// silenceMatches reports whether a silence is active at `now` and its matcher
// selects the alert. Empty matcher fields are wildcards; an all-empty matcher is
// a blanket window. Pure.
func silenceMatches(s *models.AlertSilence, a *models.Alert, now time.Time) bool {
	if now.Before(s.StartsAt) || !now.Before(s.EndsAt) {
		return false
	}
	m := s.Matcher
	if m.Service != "" && m.Service != a.ServiceName {
		return false
	}
	if m.Level != "" && !strings.EqualFold(m.Level, string(a.Level)) {
		return false
	}
	if m.MessageContains != "" && !strings.Contains(strings.ToLower(a.Message), strings.ToLower(m.MessageContains)) {
		return false
	}
	return true
}

// anySilenceMatches is true when at least one silence suppresses the alert. Pure.
func anySilenceMatches(silences []*models.AlertSilence, a *models.Alert, now time.Time) bool {
	for _, s := range silences {
		if silenceMatches(s, a, now) {
			return true
		}
	}
	return false
}

// SilenceHandler exposes CRUD for alert silences.
type SilenceHandler struct {
	repo *repository.SilenceRepository
}

func NewSilenceHandler(repo *repository.SilenceRepository) *SilenceHandler {
	return &SilenceHandler{repo: repo}
}

func (h *SilenceHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/alerts/silences", h.List)
	mux.HandleFunc("POST /api/v1/alerts/silences", h.Create)
	mux.HandleFunc("DELETE /api/v1/alerts/silences/{id}", h.Delete)
}

func (h *SilenceHandler) List(w http.ResponseWriter, r *http.Request) {
	silences, err := h.repo.ListForTenant(r.Context(), tenantFromRequest(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to list silences: "+err.Error()))
		return
	}
	if silences == nil {
		silences = []*models.AlertSilence{}
	}
	writeJSON(w, http.StatusOK, models.OK(silences))
}

func (h *SilenceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req models.AlertSilence
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.Fail("invalid request body"))
		return
	}
	now := time.Now().UTC()
	if req.StartsAt.IsZero() {
		req.StartsAt = now
	}
	if !req.EndsAt.After(req.StartsAt) {
		writeJSON(w, http.StatusBadRequest, models.Fail("ends_at must be after starts_at"))
		return
	}
	req.TenantID = tenantFromRequest(r)

	created, err := h.repo.Create(r.Context(), &req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to create silence: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusCreated, models.OK(created))
}

func (h *SilenceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("id is required"))
		return
	}
	if err := h.repo.Delete(r.Context(), tenantFromRequest(r), id); err != nil {
		writeJSON(w, http.StatusNotFound, models.Fail("silence not found"))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(map[string]string{"id": id, "deleted": "true"}))
}

// annotateSilenced marks each alert whose active silence matches, and (when
// hideSilenced) drops them. Shared by the flat and grouped alert listings.
func annotateSilenced(ctx context.Context, repo *repository.SilenceRepository, tenant string, alerts []*models.Alert, hideSilenced bool) []*models.Alert {
	if repo == nil || len(alerts) == 0 {
		return alerts
	}
	silences, err := repo.ActiveForTenant(ctx, tenant, time.Now().UTC())
	if err != nil || len(silences) == 0 {
		return alerts
	}
	now := time.Now().UTC()
	out := alerts[:0]
	for _, a := range alerts {
		if anySilenceMatches(silences, a, now) {
			a.Silenced = true
			if hideSilenced {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}
