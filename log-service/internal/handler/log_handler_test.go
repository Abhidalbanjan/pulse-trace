package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
// The regex matcher is dropped here; tests that care about it use buildQueryRE.
func buildQuery(t *testing.T, rawQuery, tenantID string) (string, error) {
	t.Helper()
	q, _, err := buildQueryRE(t, rawQuery, tenantID)
	return q, err
}

func buildQueryRE(t *testing.T, rawQuery, tenantID string) (string, *regexp.Regexp, error) {
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
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q; got %q", want, q)
		}
	}
	// The regex must NOT be emitted as `message:/…/`. Quickwit 0.8 has no regex
	// support and parses that as a wildcard, so any pattern with metacharacters
	// failed the whole query. This assertion is the regression guard: the
	// previous version of this test asserted the broken syntax was present,
	// which is why the defect survived — it checked what we generated, never
	// that the engine accepted it.
	if strings.Contains(q, "message:/") {
		t.Errorf("regex must not be emitted as a Quickwit regex clause; got %q", q)
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

func TestClampContextWindow(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", defaultContextWindow},
		{"0", 0},
		{"10", 10},
		{"200", maxContextWindow},
		{"5000", maxContextWindow}, // capped
		{"-3", defaultContextWindow},
		{"abc", defaultContextWindow}, // non-numeric degrades to default, never 400s
	}
	for _, c := range cases {
		if got := clampContextWindow(c.in); got != c.want {
			t.Errorf("clampContextWindow(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestBuildContextQuery(t *testing.T) {
	const ts = "2026-01-01T00:00:00Z"

	before := buildContextQuery("acme", "cart-service", ts, contextBefore)
	for _, want := range []string{`tenant_id:"acme"`, `service_name:"cart-service"`, "timestamp:[* TO " + ts + "]"} {
		if !strings.Contains(before, want) {
			t.Errorf("before query missing %q; got %q", want, before)
		}
	}

	after := buildContextQuery("acme", "cart-service", ts, contextAfter)
	if !strings.Contains(after, "timestamp:["+ts+" TO *]") {
		t.Errorf("after query wrong bound: %q", after)
	}

	// Tenant scope is non-negotiable on both sides — it's the isolation guarantee.
	if !strings.Contains(after, `tenant_id:"acme"`) {
		t.Errorf("after query missing tenant scope: %q", after)
	}
}

// rawLog builds a minimal Quickwit hit document for the context assembly tests.
func rawLog(t *testing.T, id, ts string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(logDocMeta{ID: id, Timestamp: ts, ServiceName: "svc"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func ids(raws []json.RawMessage) []string {
	out := make([]string, len(raws))
	for i, r := range raws {
		out[i] = hitMeta(r).ID
	}
	return out
}

func TestAssembleContext_OrdersAndDropsAnchor(t *testing.T) {
	// Quickwit returns `before` newest-first and `after` oldest-first. The anchor
	// sits on the inclusive boundary of both ranges, so it appears in each.
	beforeDesc := []json.RawMessage{
		rawLog(t, "anchor", "2026-01-01T00:00:05Z"),
		rawLog(t, "b2", "2026-01-01T00:00:04Z"),
		rawLog(t, "b1", "2026-01-01T00:00:03Z"),
	}
	afterAsc := []json.RawMessage{
		rawLog(t, "anchor", "2026-01-01T00:00:05Z"),
		rawLog(t, "a1", "2026-01-01T00:00:06Z"),
		rawLog(t, "a2", "2026-01-01T00:00:07Z"),
	}

	before, after := assembleContext("anchor", beforeDesc, afterAsc)

	// before must be chronological (oldest→newest) and anchor-free.
	if got := ids(before); len(got) != 2 || got[0] != "b1" || got[1] != "b2" {
		t.Errorf("before order/content wrong: %v", got)
	}
	if got := ids(after); len(got) != 2 || got[0] != "a1" || got[1] != "a2" {
		t.Errorf("after order/content wrong: %v", got)
	}
}

func TestAssembleContext_DedupesAcrossSides(t *testing.T) {
	// A log sharing the anchor's exact timestamp lands in both inclusive ranges;
	// it must be shown once, not duplicated across before/after.
	shared := rawLog(t, "tie", "2026-01-01T00:00:05Z")
	beforeDesc := []json.RawMessage{rawLog(t, "anchor", "2026-01-01T00:00:05Z"), shared}
	afterAsc := []json.RawMessage{rawLog(t, "anchor", "2026-01-01T00:00:05Z"), shared, rawLog(t, "a1", "2026-01-01T00:00:06Z")}

	before, after := assembleContext("anchor", beforeDesc, afterAsc)

	seen := map[string]int{}
	for _, id := range append(ids(before), ids(after)...) {
		seen[id]++
	}
	if seen["tie"] != 1 {
		t.Errorf("tie-timestamp log should appear exactly once, appeared %d times", seen["tie"])
	}
	if seen["anchor"] != 0 {
		t.Errorf("anchor must never appear in its own context window")
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

// --- regex search -----------------------------------------------------------
//
// Quickwit 0.8's query language has no regex. These cover the replacement:
// narrow with whatever literal the pattern is anchored by, then match in Go.

func TestRegexPrefilter_UsesLiteralPrefixToNarrow(t *testing.T) {
	re, clause, err := regexPrefilter(`duration_ms=[0-9]+`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Truncated to the first whole token: Quickwit's default tokenizer splits on
	// "_" and "=", so only "duration" is an indexed term. Emitting the full
	// literal would match no token and silently return zero hits.
	if clause != `message:duration*` {
		t.Errorf("expected a wildcard clause on the leading token, got %q", clause)
	}
	if !re.MatchString("request completed duration_ms=1234") {
		t.Error("matcher should match a string the pattern describes")
	}
	if re.MatchString("request completed duration_ms=abc") {
		t.Error("matcher must not match where the pattern does not")
	}
}

func TestRegexPrefilter_UnanchoredPatternDoesNotNarrow(t *testing.T) {
	// No leading literal: any clause we invented here would exclude real
	// matches, so the scan must stay wide rather than quietly lose results.
	_, clause, err := regexPrefilter(`[0-9]{3}-[0-9]{4}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clause != "" {
		t.Errorf("an unanchored pattern must not produce a narrowing clause, got %q", clause)
	}
}

func TestRegexPrefilter_ShortOrUnsafePrefixIgnored(t *testing.T) {
	for _, pattern := range []string{
		`ab[0-9]+`,  // leading token too short to be useful
		`=[0-9]+`,   // starts with punctuation, so there is no leading token
		`a b[0-9]+`, // leading token "a" is too short
	} {
		_, clause, err := regexPrefilter(pattern)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", pattern, err)
		}
		if clause != "" {
			t.Errorf("%s: expected no narrowing clause, got %q", pattern, clause)
		}
	}
}

func TestRegexPrefilter_InvalidPatternRejected(t *testing.T) {
	if _, _, err := regexPrefilter(`user_[0-9`); err == nil {
		t.Fatal("an un-compilable pattern must be rejected, not silently dropped")
	}
}

func TestFilterByRegex_MatchesAndRespectsLimit(t *testing.T) {
	hits := []json.RawMessage{
		json.RawMessage(`{"message":"duration_ms=1234 ok"}`),
		json.RawMessage(`{"message":"no numbers here"}`),
		json.RawMessage(`{"message":"duration_ms=99 ok"}`),
		json.RawMessage(`{"message":"duration_ms=7 ok"}`),
	}
	re := regexp.MustCompile(`duration_ms=[0-9]+`)

	got := filterByRegex(hits, re, 10)
	if len(got) != 3 {
		t.Errorf("expected 3 matches, got %d", len(got))
	}
	if limited := filterByRegex(hits, re, 2); len(limited) != 2 {
		t.Errorf("limit must be honoured, got %d", len(limited))
	}
}

func TestFilterByRegex_SkipsUndecodableDocuments(t *testing.T) {
	// A malformed document must not abort the whole search.
	hits := []json.RawMessage{
		json.RawMessage(`not json`),
		json.RawMessage(`{"message":"duration_ms=5"}`),
	}
	got := filterByRegex(hits, regexp.MustCompile(`duration_ms=[0-9]+`), 10)
	if len(got) != 1 {
		t.Errorf("expected the readable match only, got %d", len(got))
	}
}

func TestLeadingToken_StopsAtTokenBoundary(t *testing.T) {
	// The regression this guards: "duration_ms=" was emitted whole, matching no
	// indexed term, so a pattern that genuinely occurs returned zero hits.
	for _, tc := range []struct{ in, want string }{
		{"duration_ms=", "duration"},
		{"duration", "duration"},
		{"cache ", "cache"},
		{"=abc", ""},
		{"", ""},
	} {
		if got := leadingToken(tc.in); got != tc.want {
			t.Errorf("leadingToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
