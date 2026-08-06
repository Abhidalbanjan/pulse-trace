package handler

import (
	"encoding/json"
	"net/http"

	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
)

// AnomalyHandler exposes the per-tenant anomaly-detection tuning (F14): read the
// current thresholds/sensitivity and the on/off switch, and update them. The
// running detector picks up changes within its config-cache TTL.
type AnomalyHandler struct {
	repo *repository.AnomalyConfigRepository
}

func NewAnomalyHandler(repo *repository.AnomalyConfigRepository) *AnomalyHandler {
	return &AnomalyHandler{repo: repo}
}

func (h *AnomalyHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/anomaly/config", h.Get)
	mux.HandleFunc("PUT /api/v1/anomaly/config", h.Update)
}

// Get returns the caller tenant's anomaly config (defaults if never set).
func (h *AnomalyHandler) Get(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.repo.Get(r.Context(), tenantFromRequest(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to load anomaly config"))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(cfg))
}

// Update upserts the caller tenant's anomaly config (values are clamped server-side).
func (h *AnomalyHandler) Update(w http.ResponseWriter, r *http.Request) {
	var cfg repository.AnomalyConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, models.Fail("invalid request body"))
		return
	}
	saved, err := h.repo.Upsert(r.Context(), tenantFromRequest(r), cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to save anomaly config"))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(saved))
}
