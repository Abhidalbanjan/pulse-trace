package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/pulsetrace/log-service/internal/repository"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/models"
)

const (
	logsTopic   = "logs"
	serviceName = "log-service"
)

// LogHandler exposes HTTP endpoints for log ingestion and querying.
type LogHandler struct {
	repo     *repository.LogRepository
	producer *kafka.Producer
}

// NewLogHandler creates a handler. producer may be nil — if Kafka is unavailable
// the service degrades gracefully (logs are still persisted, just not published).
func NewLogHandler(repo *repository.LogRepository, producer *kafka.Producer) *LogHandler {
	return &LogHandler{repo: repo, producer: producer}
}

// RegisterRoutes wires up all log-service routes onto the given mux.
func (h *LogHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/logs", h.IngestLog)
	mux.HandleFunc("GET /api/v1/logs", h.ListLogs)
	mux.HandleFunc("GET /api/v1/logs/{id}", h.GetLog)
	mux.HandleFunc("GET /healthz", h.Health)
}

// IngestLog accepts a structured log entry, persists it, and publishes it to Kafka.
//
//	POST /api/v1/logs
func (h *LogHandler) IngestLog(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "log.ingest")
	defer span.End()

	var req models.CreateLogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid request body")
		writeJSON(w, http.StatusBadRequest, models.Fail("invalid request body: "+err.Error()))
		return
	}

	if req.ServiceName == "" || req.Message == "" || req.Level == "" {
		span.SetStatus(codes.Error, "missing required fields")
		writeJSON(w, http.StatusBadRequest, models.Fail("service, level, and message are required"))
		return
	}

	span.SetAttributes(
		attribute.String("log.service", req.ServiceName),
		attribute.String("log.level", string(req.Level)),
	)

	// Persist to PostgreSQL — source of truth.
	_, dbSpan := tracer.Start(ctx, "db.insert_log")
	entry, err := h.repo.Insert(ctx, &req)
	dbSpan.End()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "db insert failed")
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to store log: "+err.Error()))
		return
	}

	span.SetAttributes(attribute.String("log.id", entry.ID))

	// Publish to Kafka with trace context injected into message headers.
	if h.producer != nil {
		go func() {
			_, kafkaSpan := tracer.Start(ctx, "kafka.publish_log")
			defer kafkaSpan.End()

			payload, err := json.Marshal(entry)
			if err != nil {
				kafkaSpan.RecordError(err)
				log.Printf("failed to marshal log entry for kafka: %v", err)
				return
			}
			if err := h.producer.PublishWithContext(ctx, logsTopic, entry.ServiceName, payload); err != nil {
				kafkaSpan.RecordError(err)
				log.Printf("failed to publish log entry %s to kafka: %v", entry.ID, err)
			}
		}()
	}

	writeJSON(w, http.StatusCreated, models.OK(entry))
}

// ListLogs returns a paginated, filtered list of log entries.
//
//	GET /api/v1/logs?service=payment-service&level=ERROR&page=1&page_size=20
func (h *LogHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "log.list")
	defer span.End()

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

	writeJSON(w, http.StatusOK, models.OKPaginated(result.Entries, meta))
}

// GetLog fetches a single log entry by ID.
//
//	GET /api/v1/logs/{id}
func (h *LogHandler) GetLog(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "log.get")
	defer span.End()

	id := r.PathValue("id")
	if id == "" {
		span.SetStatus(codes.Error, "missing id")
		writeJSON(w, http.StatusBadRequest, models.Fail("id is required"))
		return
	}

	span.SetAttributes(attribute.String("log.id", id))

	entry, err := h.repo.GetByID(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "not found")
		writeJSON(w, http.StatusNotFound, models.Fail("log entry not found"))
		return
	}

	writeJSON(w, http.StatusOK, models.OK(entry))
}

// Health is a simple liveness probe.
//
//	GET /healthz
func (h *LogHandler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": serviceName})
}

// writeJSON serialises v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
