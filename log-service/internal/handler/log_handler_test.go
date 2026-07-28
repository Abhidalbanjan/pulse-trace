package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pulsetrace/shared/models"
)

// newTestHandler builds a LogHandler with just a buffered queue and no worker
// goroutines or Kafka producer, so IngestLog's parse/validate/enqueue logic can
// be exercised in isolation and the enqueued entries read straight off the
// channel.
func newTestHandler(queueSize int) *LogHandler {
	return &LogHandler{logQueue: make(chan *models.LogEntry, queueSize)}
}

func ingest(t *testing.T, h *LogHandler, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs", bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.IngestLog(rr, req)
	return rr
}

// drain non-blockingly reads everything currently on the queue.
func drain(h *LogHandler) []*models.LogEntry {
	var out []*models.LogEntry
	for {
		select {
		case e := <-h.logQueue:
			out = append(out, e)
		default:
			return out
		}
	}
}

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestIngest_SingleObject(t *testing.T) {
	h := newTestHandler(10)
	body := []byte(`{"service":"cart-service","level":"INFO","message":"hello"}`)
	rr := ingest(t, h, body, nil)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	got := drain(h)
	if len(got) != 1 {
		t.Fatalf("enqueued %d entries, want 1", len(got))
	}
	if got[0].ServiceName != "cart-service" || got[0].Message != "hello" || got[0].Level != models.LogLevelInfo {
		t.Fatalf("entry not mapped correctly: %+v", got[0])
	}
}

// Regression: the Vector edge agent batches events into a JSON array. This
// endpoint used to accept only a single object, so every batched request 400'd
// and Vector silently dropped it.
func TestIngest_BatchArray(t *testing.T) {
	h := newTestHandler(10)
	body := []byte(`[
		{"service":"a","level":"INFO","message":"m1"},
		{"service":"b","level":"ERROR","message":"m2"},
		{"service":"c","level":"WARNING","message":"m3"}
	]`)
	rr := ingest(t, h, body, nil)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	got := drain(h)
	if len(got) != 3 {
		t.Fatalf("enqueued %d entries, want 3", len(got))
	}
	if got[0].ServiceName != "a" || got[2].Message != "m3" {
		t.Fatalf("batch entries mis-ordered/mapped: %+v", got)
	}
}

// Regression: Vector's HTTP sink compresses bodies with gzip. Nothing
// decompressed them, so json.Unmarshal saw the gzip magic byte and every
// request failed.
func TestIngest_Gzip(t *testing.T) {
	h := newTestHandler(10)
	raw := []byte(`{"service":"gz","level":"INFO","message":"compressed"}`)
	rr := ingest(t, h, gzipBytes(t, raw), map[string]string{"Content-Encoding": "gzip"})

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	got := drain(h)
	if len(got) != 1 || got[0].ServiceName != "gz" {
		t.Fatalf("gzip body not ingested correctly: %+v", got)
	}
}

// The real Vector shape: gzip-compressed JSON array.
func TestIngest_GzipBatch(t *testing.T) {
	h := newTestHandler(10)
	raw := []byte(`[{"service":"a","level":"INFO","message":"m1"},{"service":"b","level":"INFO","message":"m2"}]`)
	rr := ingest(t, h, gzipBytes(t, raw), map[string]string{"Content-Encoding": "gzip"})

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if got := drain(h); len(got) != 2 {
		t.Fatalf("enqueued %d, want 2", len(got))
	}
}

func TestIngest_LevelNormalization(t *testing.T) {
	h := newTestHandler(10)
	// WARN is a common shorthand that must normalize to the canonical WARNING.
	rr := ingest(t, h, []byte(`{"service":"s","level":"WARN","message":"m"}`), nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
	got := drain(h)
	if len(got) != 1 || got[0].Level != models.LogLevelWarning {
		t.Fatalf("level not normalized WARN->WARNING: %+v", got)
	}
}

func TestIngest_MissingRequiredFields(t *testing.T) {
	h := newTestHandler(10)
	// message missing
	rr := ingest(t, h, []byte(`{"service":"s","level":"INFO"}`), nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if len(drain(h)) != 0 {
		t.Fatal("nothing should be enqueued when validation fails")
	}
}

func TestIngest_MalformedJSON(t *testing.T) {
	h := newTestHandler(10)
	rr := ingest(t, h, []byte(`{not json`), nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestIngest_EmptyArray(t *testing.T) {
	h := newTestHandler(10)
	rr := ingest(t, h, []byte(`[]`), nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for empty batch", rr.Code)
	}
	if len(drain(h)) != 0 {
		t.Fatal("empty array should enqueue nothing")
	}
}

func TestIngest_QueueFullReturns503(t *testing.T) {
	h := newTestHandler(1)
	// Pre-fill the single queue slot so the next enqueue can't proceed.
	h.logQueue <- &models.LogEntry{}
	rr := ingest(t, h, []byte(`{"service":"s","level":"INFO","message":"m"}`), nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when queue is full", rr.Code)
	}
}

// buildQuery is a helper that runs buildLogQuery against a URL's query string.
func buildQuery(t *testing.T, rawQuery, tenantID string) (string, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?"+rawQuery, nil)
	return buildLogQuery(req, tenantID)
}

func TestBuildLogQuery_TenantScopeAlwaysPresent(t *testing.T) {
	// Even with no params, the query must be scoped to the caller's tenant —
	// this is the cross-tenant isolation guarantee, not a nicety.
	q, err := buildQuery(t, "", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(q, `tenant_id:"acme"`) {
		t.Fatalf("query missing tenant scope: %q", q)
	}
}

func TestBuildLogQuery_FiltersAndRegex(t *testing.T) {
	rawQuery := "service=api&level=error&trace_id=abc&q=timeout&regex=" + url.QueryEscape("user_[0-9]+")
	q, err := buildQuery(t, rawQuery, "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		`tenant_id:"t1"`,
		`service_name:"api"`,
		`level:"ERROR"`, // level is upper-cased to match the raw-tokenized field
		`trace_id:"abc"`,
		`message:"timeout"`,
		`message:/user_[0-9]+/`,
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q; got %q", want, q)
		}
	}
}

func TestBuildLogQuery_InvalidRegexRejected(t *testing.T) {
	// An un-compilable regex must be a 400-worthy error, not a clause silently
	// dropped (which would widen the result set mid-incident).
	if _, err := buildQuery(t, "regex="+url.QueryEscape("user_[0-9"), "t1"); err == nil {
		t.Fatal("expected an error for an unbalanced regex")
	}
}

func TestBuildLogQuery_AbsoluteTimeRange(t *testing.T) {
	q, err := buildQuery(t, "start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(q, "timestamp:[2026-01-01T00:00:00Z TO 2026-01-02T00:00:00Z]") {
		t.Fatalf("absolute range clause wrong: %q", q)
	}
}

func TestBuildLogQuery_UnboundedUpper(t *testing.T) {
	q, err := buildQuery(t, "start=2026-01-01T00:00:00Z", "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(q, "timestamp:[2026-01-01T00:00:00Z TO *]") {
		t.Fatalf("unbounded upper bound wrong: %q", q)
	}
}

func TestBuildLogQuery_RelativeSince(t *testing.T) {
	q, err := buildQuery(t, "since=2h", "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(q, "timestamp:[") || !strings.Contains(q, " TO *]") {
		t.Fatalf("relative since should produce a lower-bounded open range: %q", q)
	}
	// The computed lower bound must be ~2h ago, not now or the epoch.
	lo := extractLowerBound(t, q)
	if age := time.Since(lo); age < 110*time.Minute || age > 130*time.Minute {
		t.Fatalf("since=2h produced lower bound %s ago, want ~2h", age)
	}
}

func TestBuildLogQuery_SinceDaysUnit(t *testing.T) {
	// 'd' (days) is our extension over Go's ParseDuration, which stops at hours.
	q, err := buildQuery(t, "since=7d", "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lo := extractLowerBound(t, q)
	if age := time.Since(lo); age < 167*time.Hour || age > 169*time.Hour {
		t.Fatalf("since=7d produced lower bound %s ago, want ~168h", age)
	}
}

func TestBuildLogQuery_BadTimeRejected(t *testing.T) {
	for _, bad := range []string{"start=yesterday", "end=not-a-time", "since=lots"} {
		if _, err := buildQuery(t, bad, "t1"); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}

// extractLowerBound pulls the RFC3339 lower bound out of a timestamp:[lo TO hi]
// clause so tests can assert on the computed relative time.
func extractLowerBound(t *testing.T, query string) time.Time {
	t.Helper()
	i := strings.Index(query, "timestamp:[")
	if i < 0 {
		t.Fatalf("no timestamp clause in %q", query)
	}
	rest := query[i+len("timestamp:["):]
	lo, _, ok := strings.Cut(rest, " TO ")
	if !ok {
		t.Fatalf("malformed range clause in %q", query)
	}
	ts, err := time.Parse(time.RFC3339, lo)
	if err != nil {
		t.Fatalf("lower bound %q not RFC3339: %v", lo, err)
	}
	return ts
}

func TestIngest_ValidLevelsPassThrough(t *testing.T) {
	for _, lvl := range []models.LogLevel{models.LogLevelDebug, models.LogLevelInfo, models.LogLevelWarning, models.LogLevelError, models.LogLevelFatal} {
		h := newTestHandler(10)
		body, _ := json.Marshal(models.CreateLogRequest{ServiceName: "s", Level: lvl, Message: "m"})
		rr := ingest(t, h, body, nil)
		if rr.Code != http.StatusCreated {
			t.Fatalf("level %s: status = %d, want 201", lvl, rr.Code)
		}
		got := drain(h)
		if len(got) != 1 || got[0].Level != lvl {
			t.Fatalf("level %s not preserved: %+v", lvl, got)
		}
	}
}
