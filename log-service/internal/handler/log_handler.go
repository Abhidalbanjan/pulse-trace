package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/pulsetrace/log-service/internal/repository"
	"github.com/pulsetrace/shared/models"
)

// LogHandler exposes HTTP endpoints for log ingestion and querying.
type LogHandler struct {
	repo *repository.LogRepository
}

func NewLogHandler(repo *repository.LogRepository) *LogHandler {
	return &LogHandler{repo: repo}
}

// RegisterRoutes wires up all log-service routes onto the given mux.
func (h *LogHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/logs", h.IngestLog)
	mux.HandleFunc("GET /api/v1/logs", h.ListLogs)
	mux.HandleFunc("GET /api/v1/logs/{id}", h.GetLog)
	mux.HandleFunc("GET /healthz", h.Health)
}

// IngestLog accepts a structured log entry and persists it.
//
//	POST /api/v1/logs
func (h *LogHandler) IngestLog(w http.ResponseWriter, r *http.Request) {
	var req models.CreateLogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.Fail("invalid request body: "+err.Error()))
		return
	}

	if req.ServiceName == "" || req.Message == "" || req.Level == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("service, level, and message are required"))
		return
	}

	entry, err := h.repo.Insert(r.Context(), &req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to store log: "+err.Error()))
		return
	}

	writeJSON(w, http.StatusCreated, models.OK(entry))
}

// ListLogs returns a paginated, filtered list of log entries.
//
//	GET /api/v1/logs?service=payment-service&level=ERROR&page=1&page_size=20
func (h *LogHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	params := &models.LogQueryParams{
		ServiceName: q.Get("service"),
		Level:       models.LogLevel(q.Get("level")),
		TraceID:     q.Get("trace_id"),
		From:        q.Get("from"),
		To:          q.Get("to"),
	}

	if p, err := strconv.Atoi(q.Get("page")); err == nil {
		params.Page = p
	}
	if ps, err := strconv.Atoi(q.Get("page_size")); err == nil {
		params.PageSize = ps
	}

	result, err := h.repo.Query(r.Context(), params)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("query failed: "+err.Error()))
		return
	}

	meta := &models.PaginationMeta{
		Page:       result.Page,
		PageSize:   result.PageSize,
		Total:      result.Total,
		TotalPages: repository.TotalPages(result.Total, result.PageSize),
	}

	writeJSON(w, http.StatusOK, models.OKPaginated(result.Entries, meta))
}

// GetLog fetches a single log entry by ID.
//
//	GET /api/v1/logs/{id}
func (h *LogHandler) GetLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("id is required"))
		return
	}

	entry, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, models.Fail("log entry not found"))
		return
	}

	writeJSON(w, http.StatusOK, models.OK(entry))
}

// Health is a simple liveness probe.
//
//	GET /healthz
func (h *LogHandler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "log-service"})
}

// writeJSON serialises v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
