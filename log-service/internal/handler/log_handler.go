package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"


	"github.com/pulsetrace/shared/jsonpool"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/models"
)

const (
	logsTopic   = "logs"
	serviceName = "log-service"
)

// LogHandler exposes HTTP endpoints for log ingestion and querying.
type LogHandler struct {

	producer      *kafka.Producer
	logQueue      chan *models.LogEntry
	batchSize     int
	flushInterval time.Duration
	workersWg     sync.WaitGroup
	closeChan     chan struct{}
}

// NewLogHandler creates a handler with high-performance buffered queue and worker pool.
func NewLogHandler(producer *kafka.Producer) *LogHandler {
	h := &LogHandler{
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

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}
	tenantTier := r.Header.Get("X-Tenant-Tier")
	if tenantTier == "" {
		tenantTier = "standard"
	}

	// Construct the LogEntry directly
	entry := &models.LogEntry{
		ID:          uuid.New().String(),
		TenantID:    tenantID,
		TenantTier:  tenantTier,
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

	// Ingest path writes solely to Kafka. Telemetry is indexed asynchronously
	// by Quickwit via its native Kafka source.

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

func (h *LogHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, models.Fail("Log search is now powered natively by Quickwit. Please query the Quickwit search API directly."))
}

func (h *LogHandler) GetLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, models.Fail("Log search is now powered natively by Quickwit. Please query the Quickwit search API directly."))
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
	buf := jsonpool.GetBuffer()
	defer jsonpool.PutBuffer(buf)
	if err := json.NewEncoder(buf).Encode(v); err == nil {
		w.Write(buf.Bytes())
	}
}
