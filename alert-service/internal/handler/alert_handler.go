package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/pulsetrace/alert-service/internal/repository"
	"github.com/pulsetrace/shared/jsonpool"
	"github.com/pulsetrace/shared/models"
)

const serviceName = "alert-service"

// AlertHandler exposes HTTP endpoints for querying alerts.
type AlertHandler struct {
	repo *repository.AlertRepository
}

func NewAlertHandler(repo *repository.AlertRepository) *AlertHandler {
	return &AlertHandler{repo: repo}
}

// RegisterRoutes wires up all alert-service routes onto the given mux.
func (h *AlertHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/alerts", h.ListAlerts)
	mux.HandleFunc("GET /api/v1/alerts/{id}", h.GetAlert)
	mux.HandleFunc("GET /healthz", h.Health)
}

// ListAlerts returns a paginated, filtered list of alerts.
//
//	GET /api/v1/alerts?service=payment-service&level=ERROR&page=1&page_size=20
func (h *AlertHandler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "alert.list")
	defer span.End()

	q := r.URL.Query()

	params := &models.AlertQueryParams{
		// Always tenant-scope the listing. Without this the repository's tenant
		// filter (applied only when TenantID is non-empty) is skipped and the list
		// returns every tenant's alerts.
		TenantID:    tenantFromRequest(r),
		ServiceName: q.Get("service"),
		Level:       models.LogLevel(q.Get("level")),
		From:        q.Get("from"),
		To:          q.Get("to"),
	}

	if p, err := strconv.Atoi(q.Get("page")); err == nil {
		params.Page = p
	}
	if ps, err := strconv.Atoi(q.Get("page_size")); err == nil {
		params.PageSize = ps
	}

	// Grouped view (Alerts · E1): collapse near-identical alerts into deduplicated
	// groups with a count and first/last-seen span. We group over the widest
	// single page the repository allows (capped at 200) so a storm collapses into
	// a handful of rows the operator can actually scan, rather than grouping only
	// the current small page.
	if q.Get("group") == "true" {
		params.Page = 1
		params.PageSize = 200
		result, err := h.repo.Query(ctx, params)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "query failed")
			writeJSON(w, http.StatusInternalServerError, models.Fail("query failed: "+err.Error()))
			return
		}
		groups := GroupAlerts(result.Alerts)
		span.SetAttributes(attribute.Int("alert.groups", len(groups)))
		writeJSON(w, http.StatusOK, models.OK(groups))
		return
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

	writeJSON(w, http.StatusOK, models.OKPaginated(result.Alerts, meta))
}

// GetAlert fetches a single alert by ID.
//
//	GET /api/v1/alerts/{id}
func (h *AlertHandler) GetAlert(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "alert.get")
	defer span.End()

	id := r.PathValue("id")
	if id == "" {
		span.SetStatus(codes.Error, "missing id")
		writeJSON(w, http.StatusBadRequest, models.Fail("id is required"))
		return
	}

	span.SetAttributes(attribute.String("alert.id", id))

	// Tenant-scope the read so an alert ID from one tenant can't be read by another.
	alert, err := h.repo.GetByIDForTenant(ctx, tenantFromRequest(r), id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "not found")
		writeJSON(w, http.StatusNotFound, models.Fail("alert not found"))
		return
	}

	writeJSON(w, http.StatusOK, models.OK(alert))
}

// Health is a simple liveness probe.
//
//	GET /healthz
func (h *AlertHandler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": serviceName})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	buf := jsonpool.GetBuffer()
	defer jsonpool.PutBuffer(buf)
	if err := json.NewEncoder(buf).Encode(v); err == nil {
		w.Write(buf.Bytes())
	}
}

// tenantFromRequest resolves the caller's tenant from the gateway-verified
// X-Tenant-ID header (never a raw client header — the gateway sets it from the
// JWT). Defaults to "default" so single-tenant/dev stacks keep working.
func tenantFromRequest(r *http.Request) string {
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return "default"
}
