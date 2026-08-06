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
	"net/url"
	"regexp"
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
	"github.com/pulsetrace/shared/metering"
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
	meter       *metering.Meter
}

// NewLogHandler creates a handler with high-performance buffered queue and worker pool.
func NewLogHandler(producer *kafka.Producer, quickwitURL string, meter *metering.Meter) *LogHandler {
	h := &LogHandler{
		producer:      producer,
		logQueue:      make(chan *models.LogEntry, 100000), // 100k buffered elements shock absorber
		batchSize:     2000,                                // bulk pgx/kafka batch threshold
		flushInterval: 100 * time.Millisecond,              // maximum latency threshold
		closeChan:     make(chan struct{}),
		quickwitURL:   quickwitURL,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		meter:         meter,
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
	mux.HandleFunc("GET /api/v1/logs/{id}/context", h.LogContext)
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

	// Meter accepted log records against the tenant's usage (shared Redis counters;
	// the gateway's flusher mirrors them into usage_daily).
	h.meter.Record(r.Context(), tenantID, metering.SignalLogs, int64(len(enqueued)))

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
//
// It returns an error (surfaced as 400) for malformed operator input — a bad
// timestamp or an un-compilable regex — rather than silently dropping the
// clause and returning a wider result set than the operator asked for, which
// during an incident is a dangerous way to be wrong.
func buildLogQuery(r *http.Request, tenantID string) (string, error) {
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
		// Phrase match on the full-text message field.
		clauses = append(clauses, fmt.Sprintf("message:%q", q))
	}
	if pattern := r.URL.Query().Get("regex"); pattern != "" {
		clause, err := regexClause(pattern)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, clause)
	}

	timeClause, err := timeRangeClause(r.URL.Query())
	if err != nil {
		return "", err
	}
	if timeClause != "" {
		clauses = append(clauses, timeClause)
	}

	return strings.Join(clauses, " AND "), nil
}

// regexClause builds a Quickwit regex query (message:/pattern/) against the
// full-text message field. The pattern is validated with Go's regexp engine —
// whose syntax matches the tantivy engine Quickwit uses closely enough to catch
// the mistakes an operator actually makes (unbalanced groups, bad escapes) —
// and any '/' is escaped so it can't terminate the literal early.
func regexClause(pattern string) (string, error) {
	if _, err := regexp.Compile(pattern); err != nil {
		return "", fmt.Errorf("invalid regex %q: %w", pattern, err)
	}
	escaped := strings.ReplaceAll(pattern, `/`, `\/`)
	return "message:/" + escaped + "/", nil
}

// timeRangeClause builds a Quickwit datetime range clause on the timestamp
// field from either absolute (start/end, RFC3339) or relative (since, e.g. 15m,
// 2h, 7d) bounds. Relative "since" is a convenience that sets the lower bound to
// now-duration; an explicit start always wins over it. Returns "" when no time
// bound is requested (the caller then searches all retained data).
func timeRangeClause(params url.Values) (string, error) {
	start := strings.TrimSpace(params.Get("start"))
	end := strings.TrimSpace(params.Get("end"))

	if start == "" {
		if since := strings.TrimSpace(params.Get("since")); since != "" {
			d, err := parseSince(since)
			if err != nil {
				return "", err
			}
			start = time.Now().UTC().Add(-d).Format(time.RFC3339)
		}
	} else if _, err := time.Parse(time.RFC3339, start); err != nil {
		return "", fmt.Errorf("invalid start time %q (want RFC3339): %w", start, err)
	}

	if end != "" {
		if _, err := time.Parse(time.RFC3339, end); err != nil {
			return "", fmt.Errorf("invalid end time %q (want RFC3339): %w", end, err)
		}
	}

	if start == "" && end == "" {
		return "", nil
	}

	// Quickwit range syntax: [lower TO upper], with '*' for an unbounded side.
	lower, upper := start, end
	if lower == "" {
		lower = "*"
	}
	if upper == "" {
		upper = "*"
	}
	return fmt.Sprintf("timestamp:[%s TO %s]", lower, upper), nil
}

// parseSince extends Go's duration parsing with a 'd' (days) unit, since
// operators reach for "7d" far more naturally than "168h" when scoping a log
// search. Everything else falls through to time.ParseDuration.
func parseSince(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || days < 0 {
			return 0, fmt.Errorf("invalid since %q (want e.g. 15m, 2h, 7d)", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid since %q (want e.g. 15m, 2h, 7d)", s)
	}
	return d, nil
}

// ListLogs searches recent logs via Quickwit, scoped to the caller's tenant.
//
//	GET /api/v1/logs?service=&level=&trace_id=&q=&regex=&start=&end=&since=&limit=
//
// q is a phrase match on the message; regex is a full regex on the message
// (message:/pattern/). Time is bounded by absolute start/end (RFC3339) or a
// relative since (e.g. 15m, 2h, 7d); an explicit start wins over since.
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

	query, err := buildLogQuery(r, tenantID)
	if err != nil {
		span.SetStatus(codes.Error, "invalid query params")
		writeJSON(w, http.StatusBadRequest, models.Fail(err.Error()))
		return
	}

	req := quickwitSearchRequest{
		Query:       query,
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

const (
	// defaultContextWindow / maxContextWindow bound how many neighbouring log
	// lines the surrounding-context view returns on each side of the anchor.
	// The cap protects the search backend from an operator-supplied huge window.
	defaultContextWindow = 25
	maxContextWindow     = 200

	contextBefore = "before"
	contextAfter  = "after"
)

// logDocMeta is the minimal projection of a log document the context view needs
// to anchor the surrounding window: its own id (to dedupe), its timestamp (the
// pivot of the range query), and its service (context is same-service by
// design — interleaving every service's logs would defeat the purpose).
type logDocMeta struct {
	ID          string `json:"id"`
	Timestamp   string `json:"timestamp"`
	ServiceName string `json:"service_name"`
}

// hitMeta extracts the anchoring fields from a raw Quickwit hit. A malformed
// hit yields a zero value (empty fields), which callers treat as "unusable".
func hitMeta(raw json.RawMessage) logDocMeta {
	var m logDocMeta
	_ = json.Unmarshal(raw, &m)
	return m
}

// logContextResponse is the surrounding-context payload: the anchor log plus its
// chronological neighbours on the same service. `before` runs oldest→newest and
// ends just before the anchor; `after` runs newest-after→latest.
type logContextResponse struct {
	Before []json.RawMessage `json:"before"`
	Anchor json.RawMessage   `json:"anchor"`
	After  []json.RawMessage `json:"after"`
}

// clampContextWindow parses a per-side window size, falling back to the default
// for empty/invalid input and capping at maxContextWindow. It never errors — a
// nonsensical value degrades to a sane window rather than 400-ing an operator
// who is mid-incident.
func clampContextWindow(raw string) int {
	if raw == "" {
		return defaultContextWindow
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return defaultContextWindow
	}
	if n > maxContextWindow {
		return maxContextWindow
	}
	return n
}

// buildContextQuery builds the Quickwit query for one side of the context
// window: same tenant, same service, timestamps on the requested side of the
// anchor. The range is inclusive of the anchor's own timestamp (so the anchor —
// and any log sharing its exact timestamp — is captured); assembleContext then
// removes the anchor and de-duplicates. Values are quoted so a service name with
// odd characters can't break the query.
func buildContextQuery(tenantID, service, anchorTS, direction string) string {
	base := fmt.Sprintf("tenant_id:%q AND service_name:%q", tenantID, service)
	if direction == contextBefore {
		return base + fmt.Sprintf(" AND timestamp:[* TO %s]", anchorTS)
	}
	return base + fmt.Sprintf(" AND timestamp:[%s TO *]", anchorTS)
}

// assembleContext turns the two raw Quickwit result sets into chronological
// neighbour lists. `beforeDesc` arrives newest-first (Quickwit -timestamp) and
// is reversed to chronological order; `afterAsc` arrives oldest-first. The
// anchor is dropped and every log is emitted at most once — a log sharing the
// anchor's exact timestamp can appear on both sides of the inclusive range, and
// showing it twice would mislead an operator reading the window.
func assembleContext(anchorID string, beforeDesc, afterAsc []json.RawMessage) (before, after []json.RawMessage) {
	seen := map[string]bool{}
	if anchorID != "" {
		seen[anchorID] = true
	}
	keep := func(raw json.RawMessage) bool {
		id := hitMeta(raw).ID
		if id == "" {
			return true // can't dedupe an id-less hit; keep it rather than drop data
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		return true
	}
	for i := len(beforeDesc) - 1; i >= 0; i-- { // reverse newest-first → chronological
		if keep(beforeDesc[i]) {
			before = append(before, beforeDesc[i])
		}
	}
	for _, raw := range afterAsc {
		if keep(raw) {
			after = append(after, raw)
		}
	}
	return before, after
}

// LogContext returns the log immediately surrounding a given log on the same
// service — the "view in context" an operator reaches for after landing on a
// single error line. It resolves the anchor (tenant-scoped), then fetches N
// neighbours on each side by timestamp.
//
//	GET /api/v1/logs/{id}/context?before=N&after=N
func (h *LogHandler) LogContext(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "log.context")
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

	before := clampContextWindow(r.URL.Query().Get("before"))
	after := clampContextWindow(r.URL.Query().Get("after"))

	// 1. Resolve the anchor, tenant-scoped — never trust a client-supplied
	//    service/timestamp for the window; derive them from the stored document.
	anchorHits, err := h.searchQuickwit(ctx, quickwitSearchRequest{
		Query:   fmt.Sprintf("id:%q AND tenant_id:%q", id, tenantID),
		MaxHits: 1,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "quickwit anchor lookup failed")
		log.Printf("log-service: LogContext anchor lookup failed: %v", err)
		writeJSON(w, http.StatusBadGateway, models.Fail("log search backend unavailable: "+err.Error()))
		return
	}
	if len(anchorHits) == 0 {
		writeJSON(w, http.StatusNotFound, models.Fail("log not found"))
		return
	}
	anchor := anchorHits[0]
	meta := hitMeta(anchor)
	if meta.ServiceName == "" || meta.Timestamp == "" {
		writeJSON(w, http.StatusBadGateway, models.Fail("anchor log is missing service/timestamp; cannot build context"))
		return
	}
	if _, perr := time.Parse(time.RFC3339, meta.Timestamp); perr != nil {
		if _, perr = time.Parse(time.RFC3339Nano, meta.Timestamp); perr != nil {
			writeJSON(w, http.StatusBadGateway, models.Fail("anchor timestamp is not RFC3339"))
			return
		}
	}

	// 2. Fetch each side. Over-fetch by one to absorb the anchor itself, which
	//    sits on the inclusive boundary of both ranges.
	beforeHits, err := h.searchQuickwit(ctx, quickwitSearchRequest{
		Query:       buildContextQuery(tenantID, meta.ServiceName, meta.Timestamp, contextBefore),
		MaxHits:     before + 1,
		SortByField: "-timestamp",
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "quickwit before-context search failed")
		log.Printf("log-service: LogContext before search failed: %v", err)
		writeJSON(w, http.StatusBadGateway, models.Fail("log search backend unavailable: "+err.Error()))
		return
	}
	afterHits, err := h.searchQuickwit(ctx, quickwitSearchRequest{
		Query:       buildContextQuery(tenantID, meta.ServiceName, meta.Timestamp, contextAfter),
		MaxHits:     after + 1,
		SortByField: "timestamp",
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "quickwit after-context search failed")
		log.Printf("log-service: LogContext after search failed: %v", err)
		writeJSON(w, http.StatusBadGateway, models.Fail("log search backend unavailable: "+err.Error()))
		return
	}

	b, a := assembleContext(meta.ID, beforeHits, afterHits)
	// Trim back to the requested per-side window, keeping the neighbours closest
	// to the anchor (the tail of `before`, the head of `after`).
	if len(b) > before {
		b = b[len(b)-before:]
	}
	if len(a) > after {
		a = a[:after]
	}

	span.SetAttributes(
		attribute.String("log.id", meta.ID),
		attribute.String("log.service", meta.ServiceName),
		attribute.Int("context.before", len(b)),
		attribute.Int("context.after", len(a)),
	)
	writeJSON(w, http.StatusOK, models.OK(logContextResponse{Before: b, Anchor: anchor, After: a}))
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
