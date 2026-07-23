package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
