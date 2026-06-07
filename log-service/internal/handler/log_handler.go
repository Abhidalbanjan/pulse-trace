package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	repo          *repository.ClickHouseLogRepository
	producer      *kafka.Producer
	logQueue      chan *models.LogEntry
	batchSize     int
	flushInterval time.Duration
	workersWg     sync.WaitGroup
	closeChan     chan struct{}
}

// NewLogHandler creates a handler with high-performance buffered queue and worker pool.
func NewLogHandler(repo *repository.ClickHouseLogRepository, producer *kafka.Producer) *LogHandler {
	h := &LogHandler{
		repo:          repo,
		producer:      producer,
		logQueue:      make(chan *models.LogEntry, 100000), // 100k buffered elements shock absorber
		batchSize:     2000,                               // bulk pgx/kafka batch threshold
		flushInterval: 100 * time.Millisecond,             // maximum latency threshold
		closeChan:     make(chan struct{}),
	}

	// Spin up 8 concurrent high-throughput ingestion workers
	for i := 0; i < 8; i++ {
		h.workersWg.Add(1)
		go h.worker(i)
	}

	log.Printf("log-service: initialized high-throughput buffered ingest pipeline (8 workers, batch=%d, latency=%v)", h.batchSize, h.flushInterval)
	return h
}

// RegisterRoutes wires up all log-service routes onto the given mux.
func (h *LogHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/logs", h.IngestLog)
	mux.HandleFunc("GET /api/v1/logs", h.ListLogs)
	mux.HandleFunc("GET /api/v1/logs/{id}", h.GetLog)
	mux.HandleFunc("GET /healthz", h.Health)
}

// IngestLog accepts a structured log entry, enqueues it, and responds immediately.
//
//	POST /api/v1/logs
func (h *LogHandler) IngestLog(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	_, span := tracer.Start(r.Context(), "log.ingest")
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

	// Normalize log level to match DB check constraints
	levelStr := strings.ToUpper(string(req.Level))
	if levelStr == "WARN" {
		req.Level = models.LogLevelWarning
	} else if levelStr == "ERR" {
		req.Level = models.LogLevelError
	} else if levelStr == "INF" {
		req.Level = models.LogLevelInfo
	} else if levelStr == "DBG" {
		req.Level = models.LogLevelDebug
	}

	span.SetAttributes(
		attribute.String("log.service", req.ServiceName),
		attribute.String("log.level", string(req.Level)),
	)

	// Construct the LogEntry directly
	entry := &models.LogEntry{
		ID:          uuid.New().String(),
		ServiceName: req.ServiceName,
		Level:       req.Level,
		Message:     req.Message,
		TraceID:     req.TraceID,
		SpanID:      req.SpanID,
		CreatedAt:   time.Now().UTC(),
	}

	if req.Timestamp != nil {
		entry.Timestamp = req.Timestamp.UTC()
	} else {
		entry.Timestamp = entry.CreatedAt
	}

	if len(req.Metadata) > 0 {
		b, err := json.Marshal(req.Metadata)
		if err != nil {
			span.RecordError(err)
			log.Printf("failed to marshal metadata: %v", err)
		} else {
			entry.Metadata = string(b)
		}
	}

	span.SetAttributes(attribute.String("log.id", entry.ID))

	// Enqueue in the high-speed shock-absorber channel
	select {
	case h.logQueue <- entry:
		writeJSON(w, http.StatusCreated, models.OK(entry))
	default:
		span.SetStatus(codes.Error, "ingestion queue full")
		writeJSON(w, http.StatusServiceUnavailable, models.Fail("ingestion queue is temporarily full under extreme peak load"))
	}
}

// worker loops continuously, draining the queue and flushing batches.
func (h *LogHandler) worker(id int) {
	defer h.workersWg.Done()

	ticker := time.NewTicker(h.flushInterval)
	defer ticker.Stop()

	var batch []*models.LogEntry

	flush := func() {
		if len(batch) == 0 {
			return
		}
		h.flushBatch(batch)
		batch = nil
	}

	for {
		select {
		case entry, ok := <-h.logQueue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, entry)
			if len(batch) >= h.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// flushBatch aggregates DB copy and Kafka publishing.
func (h *LogHandler) flushBatch(batch []*models.LogEntry) {
	ctx := context.Background()
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(ctx, "log.flush_batch")
	defer span.End()

	span.SetAttributes(attribute.Int("batch.size", len(batch)))

	// Ingest path writes solely to Kafka. Telemetry is written asynchronously
	// to ClickHouse via the Kafka batch consumer group.

	// 2. Publish batch to Kafka in a single TCP operation
	if h.producer != nil {
		_, kafkaSpan := tracer.Start(ctx, "kafka.publish_batch")
		err := h.producer.PublishBatch(ctx, logsTopic, batch)
		kafkaSpan.End()
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "kafka batch publish failed")
			log.Printf("ERROR: worker failed to publish %d logs to Kafka: %v", len(batch), err)
		}
	}
}

// Close stops the handler, closes the ingestion queue, and flushes any outstanding logs cleanly.
func (h *LogHandler) Close() {
	log.Println("closing log-service log handler queue...")
	close(h.closeChan)
	close(h.logQueue)
	h.workersWg.Wait()
	log.Println("log-service log handler shutdown complete.")
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
