package channels

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pulsetrace/shared/models"
)

// Handler is the HTTP API for managing a tenant's delivery channels and sending
// a test notification. Mounted in notification-service and proxied by the
// gateway under /api/v1/notification-channels (admin-gated there).
type Handler struct {
	repo   *Repository
	client *http.Client
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo, client: &http.Client{Timeout: 10 * time.Second}}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/notification-channels", h.List)
	mux.HandleFunc("POST /api/v1/notification-channels", h.Create)
	mux.HandleFunc("PUT /api/v1/notification-channels/{id}", h.Update)
	mux.HandleFunc("DELETE /api/v1/notification-channels/{id}", h.Delete)
	mux.HandleFunc("POST /api/v1/notification-channels/{id}/test", h.Test)
}

// tenantOf returns the gateway-verified tenant. Never trusted from the client
// directly — the gateway sets X-Tenant-ID from the JWT and strips client copies.
func tenantOf(r *http.Request) string {
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return "default"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListForAPI(r.Context(), tenantOf(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list channels"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type channelRequest struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Config  map[string]string `json:"config"`
	Enabled *bool             `json:"enabled"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req channelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if !ValidType(req.Type) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be one of: slack, email, pagerduty, opsgenie, webhook"})
		return
	}
	if missing := MissingRequired(req.Type, req.Config); len(missing) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing required config: " + strings.Join(missing, ", ")})
		return
	}
	if !EncryptionConfigured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "channel secrets cannot be stored: set CHANNEL_ENCRYPTION_KEY on notification-service"})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ch := &Channel{TenantID: tenantOf(r), Name: strings.TrimSpace(req.Name), Type: req.Type, Config: req.Config, Enabled: enabled}
	created, err := h.repo.Create(r.Context(), ch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create channel"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": created.Redacted()})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req channelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	updated, err := h.repo.Update(r.Context(), tenantOf(r), id, strings.TrimSpace(req.Name), enabled, req.Config)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update channel"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated.Redacted()})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.Delete(r.Context(), tenantOf(r), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete channel"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Test sends a synthetic notification through the channel so an admin can verify
// delivery without waiting for a real incident.
func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
	ch, err := h.repo.Get(r.Context(), tenantOf(r), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load channel"})
		return
	}
	event := &models.NotificationEvent{
		ID:         "test-" + ch.ID,
		IncidentID: "test",
		Title:      "PulseTrace test notification",
		Body:       "This is a test alert sent from PulseTrace to verify channel delivery. No action needed.",
		Severity:   models.LogLevelWarning,
		Services:   []string{"pulsetrace"},
		CreatedAt:  time.Now().UTC(),
	}
	if err := Deliver(r.Context(), h.client, *ch, event); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "delivery failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"status": "sent"}})
}
