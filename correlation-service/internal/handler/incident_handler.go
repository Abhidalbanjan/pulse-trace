package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
)

const serviceName = "correlation-service"

// IncidentHandler exposes HTTP endpoints for querying incidents and timelines.
type IncidentHandler struct {
	repo *repository.IncidentRepository
}

func NewIncidentHandler(repo *repository.IncidentRepository) *IncidentHandler {
	return &IncidentHandler{repo: repo}
}

// RegisterRoutes wires up all correlation-service routes.
func (h *IncidentHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/incidents", h.ListIncidents)
	mux.HandleFunc("GET /api/v1/incidents/{id}", h.GetIncident)
	mux.HandleFunc("GET /api/v1/incidents/{id}/timeline", h.GetTimeline)
	mux.HandleFunc("GET /healthz", h.Health)
}

// ListIncidents returns a paginated list of incidents.
//
//	GET /api/v1/incidents?status=OPEN&severity=ERROR&page=1&page_size=20
func (h *IncidentHandler) ListIncidents(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "incident.list")
	defer span.End()

	q := r.URL.Query()
	params := &models.IncidentQueryParams{
		Status:   q.Get("status"),
		Severity: q.Get("severity"),
		Service:  q.Get("service"),
		From:     q.Get("from"),
		To:       q.Get("to"),
	}
	if p, err := strconv.Atoi(q.Get("page")); err == nil {
		params.Page = p
	}
	if ps, err := strconv.Atoi(q.Get("page_size")); err == nil {
		params.PageSize = ps
	}

	result, err := h.repo.Query(ctx, params)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")
		writeJSON(w, http.StatusInternalServerError, models.Fail("query failed: "+err.Error()))
		return
	}

	meta := &models.PaginationMeta{
		Page:       result.Page,
		PageSize:   result.PageSize,
		Total:      result.Total,
		TotalPages: repository.TotalPages(result.Total, result.PageSize),
	}
	writeJSON(w, http.StatusOK, models.OKPaginated(result.Incidents, meta))
}

// GetIncident fetches a single incident by ID.
//
//	GET /api/v1/incidents/{id}
func (h *IncidentHandler) GetIncident(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "incident.get")
	defer span.End()

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("id is required"))
		return
	}
	span.SetAttributes(attribute.String("incident.id", id))

	incident, err := h.repo.GetByID(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "not found")
		writeJSON(w, http.StatusNotFound, models.Fail("incident not found"))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(incident))
}

// GetTimeline returns the ordered event timeline for an incident.
//
//	GET /api/v1/incidents/{id}/timeline
func (h *IncidentHandler) GetTimeline(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "incident.timeline")
	defer span.End()

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("id is required"))
		return
	}
	span.SetAttributes(attribute.String("incident.id", id))

	events, err := h.repo.Timeline(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "timeline failed")
		writeJSON(w, http.StatusNotFound, models.Fail("incident not found"))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(events))
}

// Health is a simple liveness probe.
func (h *IncidentHandler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": serviceName})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
