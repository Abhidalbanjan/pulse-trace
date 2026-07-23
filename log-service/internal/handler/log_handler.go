package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
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


	"github.com/pulsetrace/shared/jsonpool"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/models"
)

const (
	logsTopic     = "logs"
	serviceName   = "log-service"
	quickwitIndex = "pulsetrace-logs" // must match quickwit/logs-index.yaml's index_id
)

// LogHandler exposes HTTP endpoints for log ingestion and querying.
type LogHandler struct {

	producer      *kafka.Producer
	logQueue      chan *models.LogEntry
	batchSize     int
	flushInterval time.Duration
	workersWg     sync.WaitGroup
	closeChan     chan struct{}

	// quickwitURL backs ListLogs/GetLog. Log search itself runs through
	// Quickwit's native Kafka source (see quickwit/logs-index.yaml) — these
	// two endpoints are a server-side query convenience for API consumers who
	// shouldn't need to know Quickwit exists, not the ingestion path.
	quickwitURL string
	httpClient  *http.Client
}

// NewLogHandler creates a handler with high-performance buffered queue and worker pool.
func NewLogHandler(producer *kafka.Producer, quickwitURL string) *LogHandler {
	h := &LogHandler{
		producer:      producer,
		logQueue:      make(chan *models.LogEntry, 100000), // 100k buffered elements shock absorber
		batchSize:     2000,                               // bulk pgx/kafka batch threshold
		flushInterval: 100 * time.Millisecond,             // maximum latency threshold
		closeChan:     make(chan struct{}),
		quickwitURL:   quickwitURL,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
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

// IngestLog accepts either a single structured log entry or a JSON array of
// entries, enqueues them, and responds immediately.
//
// Two things matter in practice because of how the Vector edge agent
// (vector/vector.toml) forwards logs here:
//   - Batching: Vector batches up to 5000 events per HTTP POST for ingest
//     efficiency, so its request bodies arrive as a JSON array, not a lone
//     object. This endpoint previously only accepted a single object, so
//     every batched request 400'd.
//   - Compression: Vector's sink is configured with `compression = "gzip"`,
//     so those request bodies are gzip-compressed with a `Content-Encoding:
//     gzip` header. Nothing in the gateway proxy chain or here decompressed
//     it, so json.Unmarshal was handed raw gzip bytes and failed instantly
//     ("invalid character '\x1f'" - the gzip magic byte).
//
// Both silently dropped every log Vector ever forwarded (its HTTP sink
// doesn't retry 400s) - logs sent through the edge agent never reached
// Kafka/Quickwit at all.
//
//	POST /api/v1/logs
func (h *LogHandler) IngestLog(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	_, span := tracer.Start(r.Context(), "log.ingest")
	defer span.End()

	bodyReader := io.Reader(r.Body)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "failed to decompress gzip request body")
			writeJSON(w, http.StatusBadRequest, models.Fail("failed to decompress gzip request body: "+err.Error()))
			return
		}
		defer gz.Close()
		bodyReader = gz
	}

	body, err := io.ReadAll(bodyReader)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to read request body")
		writeJSON(w, http.StatusBadRequest, models.Fail("failed to read request body: "+err.Error()))
		return
	}

	trimmed := bytes.TrimLeft(body, " \t\r\n")
	var reqs []models.CreateLogRequest
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &reqs); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "invalid request body")
			writeJSON(w, http.StatusBadRequest, models.Fail("invalid request body: "+err.Error()))
			return
		}
	} else {
		var single models.CreateLogRequest
		if err := json.Unmarshal(trimmed, &single); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "invalid request body")
			writeJSON(w, http.StatusBadRequest, models.Fail("invalid request body: "+err.Error()))
			return
		}
		reqs = []models.CreateLogRequest{single}
	}

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}
	tenantTier := r.Header.Get("X-Tenant-Tier")
	if tenantTier == "" {
		tenantTier = "standard"
	}

	span.SetAttributes(attribute.Int("log.batch_size", len(reqs)))

	entries := make([]*models.LogEntry, 0, len(reqs))
	for _, req := range reqs {
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

		entries = append(entries, entry)
	}

	// Enqueue in the high-speed shock-absorber channel
	enqueued := make([]*models.LogEntry, 0, len(entries))
	for _, entry := range entries {
		select {
		case h.logQueue <- entry:
			enqueued = append(enqueued, entry)
		default:
			span.SetStatus(codes.Error, "ingestion queue full")
			writeJSON(w, http.StatusServiceUnavailable, models.Fail("ingestion queue is temporarily full under extreme peak load"))
			return
		}
	}

	if len(enqueued) == 1 {
		span.SetAttributes(attribute.String("log.id", enqueued[0].ID))
		writeJSON(w, http.StatusCreated, models.OK(enqueued[0]))
		return
	}
	writeJSON(w, http.StatusCreated, models.OK(enqueued))
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

// quickwitSearchRequest mirrors the subset of Quickwit's search API this
// handler uses. See https://quickwit.io/docs/reference/rest-api for the
// full shape; max_hits/sort_by_field/query are all it needs here.
type quickwitSearchRequest struct {
	Query       string `json:"query"`
	MaxHits     int    `json:"max_hits"`
	SortByField string `json:"sort_by_field,omitempty"`
}

type quickwitSearchResponse struct {
	Hits []json.RawMessage `json:"hits"`
}

// buildLogQuery translates ListLogs' query params into a Quickwit query
// string. Every clause is deliberately quoted/escaped rather than
// concatenated raw, since these values come straight from the request.
func buildLogQuery(r *http.Request, tenantID string) string {
	clauses := []string{fmt.Sprintf("tenant_id:%q", tenantID)}

	if svc := r.URL.Query().Get("service"); svc != "" {
		clauses = append(clauses, fmt.Sprintf("service_name:%q", svc))
	}
	if level := r.URL.Query().Get("level"); level != "" {
		clauses = append(clauses, fmt.Sprintf("level:%q", strings.ToUpper(level)))
	}
	if traceID := r.URL.Query().Get("trace_id"); traceID != "" {
		clauses = append(clauses, fmt.Sprintf("trace_id:%q", traceID))
	}
	if q := r.URL.Query().Get("q"); q != "" {
		clauses = append(clauses, fmt.Sprintf("message:%q", q))
	}

	return strings.Join(clauses, " AND ")
}

// ListLogs searches recent logs via Quickwit, scoped to the caller's tenant.
//
//	GET /api/v1/logs?service=&level=&trace_id=&q=&limit=
func (h *LogHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "log.list")
	defer span.End()

	if h.quickwitURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, models.Fail("log search is not configured (QUICKWIT_URL unset)"))
		return
	}

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	req := quickwitSearchRequest{
		Query:       buildLogQuery(r, tenantID),
		MaxHits:     limit,
		SortByField: "-timestamp",
	}

	hits, err := h.searchQuickwit(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "quickwit search failed")
		log.Printf("log-service: ListLogs search failed: %v", err)
		writeJSON(w, http.StatusBadGateway, models.Fail("log search backend unavailable: "+err.Error()))
		return
	}

	writeJSON(w, http.StatusOK, models.OK(hits))
}

// GetLog fetches a single log entry by ID via Quickwit.
//
//	GET /api/v1/logs/{id}
func (h *LogHandler) GetLog(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "log.get")
	defer span.End()

	if h.quickwitURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, models.Fail("log search is not configured (QUICKWIT_URL unset)"))
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("log id is required"))
		return
	}

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	req := quickwitSearchRequest{
		Query:   fmt.Sprintf("id:%q AND tenant_id:%q", id, tenantID),
		MaxHits: 1,
	}

	hits, err := h.searchQuickwit(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "quickwit search failed")
		log.Printf("log-service: GetLog search failed: %v", err)
		writeJSON(w, http.StatusBadGateway, models.Fail("log search backend unavailable: "+err.Error()))
		return
	}

	if len(hits) == 0 {
		writeJSON(w, http.StatusNotFound, models.Fail("log not found"))
		return
	}

	writeJSON(w, http.StatusOK, models.OK(hits[0]))
}

// searchQuickwit posts a search request to Quickwit's REST API and returns
// the raw hit documents.
func (h *LogHandler) searchQuickwit(ctx context.Context, sreq quickwitSearchRequest) ([]json.RawMessage, error) {
	body, err := json.Marshal(sreq)
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/%s/search", strings.TrimRight(h.quickwitURL, "/"), quickwitIndex)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build search request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call quickwit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // index not created yet / no data ingested
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quickwit returned status %d", resp.StatusCode)
	}

	var sresp quickwitSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sresp); err != nil {
		return nil, fmt.Errorf("decode quickwit response: %w", err)
	}
	return sresp.Hits, nil
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
