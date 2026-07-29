package ingestproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/pulsetrace/shared/models"
)

// ── Datadog v0.5 traces ─────────────────────────────────────────────────────

func TestDatadogTraces_V05StringTable(t *testing.T) {
	// Build a v0.5 payload by hand: a string table + one span whose fields are
	// indices into it. This is what a modern DD agent sends by default.
	strTable := []string{"", "api", "http.request", "GET /users", "web", "http.method", "GET"}
	idx := map[string]uint32{}
	for i, s := range strTable {
		idx[s] = uint32(i)
	}
	span := ddSpanV05{
		Service: idx["api"], Name: idx["http.request"], Resource: idx["GET /users"],
		TraceID: 7, SpanID: 8, ParentID: 0, Start: 1_700_000_000_000_000_000, Duration: 3_000_000,
		Error: 1, Type: idx["web"],
		Meta:    map[uint32]uint32{idx["http.method"]: idx["GET"]},
		Metrics: map[uint32]float64{},
	}
	payload := ddV05Payload{Strings: strTable, Traces: [][]ddSpanV05{{span}}}
	packed, err := msgpack.Marshal(&payload)
	if err != nil {
		t.Fatalf("marshal v0.5: %v", err)
	}

	f := &fakeForwarder{}
	p := newProxy(f, true)
	req := httptest.NewRequest(http.MethodPost, "/v0.5/traces", bytes.NewReader(packed))
	req.Header.Set("Content-Type", "application/msgpack")
	req.Header.Set("DD-API-KEY", "k-acme")
	rr := httptest.NewRecorder()
	p.DatadogTraces(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	rs := f.traces.GetResourceSpans()
	if len(rs) != 1 {
		t.Fatalf("expected 1 resource span, got %d", len(rs))
	}
	sp := rs[0].GetScopeSpans()[0].GetSpans()[0]
	if sp.GetName() != "GET /users" {
		t.Errorf("v0.5 span name = %q, want resolved 'GET /users'", sp.GetName())
	}
	if rs[0].GetResource().GetAttributes()[0].GetValue().GetStringValue() != "api" {
		t.Errorf("service.name not resolved from string table")
	}
	// meta index pair resolved to http.method=GET
	var sawMethod bool
	for _, a := range sp.GetAttributes() {
		if a.GetKey() == "http.method" && a.GetValue().GetStringValue() == "GET" {
			sawMethod = true
		}
	}
	if !sawMethod {
		t.Error("v0.5 meta indices not resolved to http.method=GET")
	}
}

// ── Datadog metrics ─────────────────────────────────────────────────────────

func TestDatadogSeries_V1(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true)

	body := `{"series":[
		{"metric":"system.cpu.user","points":[[1700000000,42.5]],"type":"gauge","host":"web01","tags":["env:prod"]},
		{"metric":"requests.count","points":[[1700000000,7]],"type":"count","tags":["route:/health"]}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/series", strings.NewReader(body))
	req.Header.Set("DD-API-KEY", "k-acme")
	rr := httptest.NewRecorder()
	p.DatadogSeries(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if f.tenantID != "acme" {
		t.Fatalf("tenant not resolved, got %q", f.tenantID)
	}
	ms := f.metrics.GetResourceMetrics()[0].GetScopeMetrics()[0].GetMetrics()
	if len(ms) != 2 {
		t.Fatalf("expected 2 metrics, got %d", len(ms))
	}
	byName := map[string]*metricspb.Metric{}
	for _, m := range ms {
		byName[m.GetName()] = m
	}
	// gauge → Gauge
	if byName["system.cpu.user"].GetGauge() == nil {
		t.Error("gauge metric should map to OTLP Gauge")
	}
	dp := byName["system.cpu.user"].GetGauge().GetDataPoints()[0]
	if dp.GetAsDouble() != 42.5 || dp.GetTimeUnixNano() != 1700000000*1e9 {
		t.Errorf("gauge datapoint wrong: v=%v ts=%d", dp.GetAsDouble(), dp.GetTimeUnixNano())
	}
	// count → monotonic delta Sum
	sum := byName["requests.count"].GetSum()
	if sum == nil || !sum.GetIsMonotonic() || sum.GetAggregationTemporality() != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA {
		t.Error("count metric should map to a monotonic delta Sum")
	}
}

func TestDatadogSeries_V2(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true)

	body := `{"series":[{"metric":"mem.used","type":3,"points":[{"timestamp":1700000000,"value":128}],"resources":[{"name":"web01","type":"host"}],"tags":["env:prod"],"unit":"megabyte"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v2/series", strings.NewReader(body))
	req.Header.Set("DD-API-KEY", "k-acme")
	rr := httptest.NewRecorder()
	p.DatadogSeries(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	m := f.metrics.GetResourceMetrics()[0].GetScopeMetrics()[0].GetMetrics()[0]
	if m.GetName() != "mem.used" || m.GetUnit() != "megabyte" || m.GetGauge() == nil {
		t.Fatalf("v2 gauge metric wrong: %+v", m)
	}
	// host resource → host.name attribute on the datapoint
	var sawHost bool
	for _, a := range m.GetGauge().GetDataPoints()[0].GetAttributes() {
		if a.GetKey() == "host.name" && a.GetValue().GetStringValue() == "web01" {
			sawHost = true
		}
	}
	if !sawHost {
		t.Error("v2 host resource not mapped to host.name attribute")
	}
}

// ── Datadog logs ────────────────────────────────────────────────────────────

func TestDatadogLogs(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true)

	body := `[{"message":"boom","ddsource":"nginx","service":"web","hostname":"h1","ddtags":"env:prod,team:core","status":"error","timestamp":1700000000000}]`
	req := httptest.NewRequest(http.MethodPost, "/api/v2/logs", strings.NewReader(body))
	req.Header.Set("DD-API-KEY", "k-acme")
	rr := httptest.NewRecorder()
	p.DatadogLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	rec := f.logs.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
	if rec.GetBody().GetStringValue() != "boom" || rec.GetSeverityText() != "error" {
		t.Errorf("log body/severity wrong: %q/%q", rec.GetBody().GetStringValue(), rec.GetSeverityText())
	}
	if rec.GetTimeUnixNano() != 1700000000000*1e6 {
		t.Errorf("log timestamp (ms→ns) wrong: %d", rec.GetTimeUnixNano())
	}
	attrs := map[string]string{}
	for _, a := range rec.GetAttributes() {
		attrs[a.GetKey()] = a.GetValue().GetStringValue()
	}
	if attrs["service.name"] != "web" || attrs["team"] != "core" || attrs["datadog.source"] != "nginx" {
		t.Errorf("log attributes not mapped from ddtags/fields: %v", attrs)
	}
}

// ── Splunk metrics ──────────────────────────────────────────────────────────

func TestSplunkHEC_RoutesMetricsAndLogs(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true)

	// One metric event (single-metric format) and one plain log event.
	body := `{"time":1700000000,"event":"metric","fields":{"metric_name":"cpu.usage","_value":73.2,"region":"us"}}
{"event":"a normal log line","source":"app"}`
	req := httptest.NewRequest(http.MethodPost, "/services/collector", strings.NewReader(body))
	req.Header.Set("Authorization", "Splunk k-acme")
	rr := httptest.NewRecorder()
	p.SplunkHEC(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	// Metric routed to the metrics signal.
	if f.metrics == nil {
		t.Fatal("metric event was not forwarded as a metric")
	}
	m := f.metrics.GetResourceMetrics()[0].GetScopeMetrics()[0].GetMetrics()[0]
	if m.GetName() != "cpu.usage" || m.GetGauge().GetDataPoints()[0].GetAsDouble() != 73.2 {
		t.Errorf("splunk metric wrong: %+v", m)
	}
	// The dimension travelled as an attribute; the log went to the logs signal.
	if f.logs == nil || len(f.logs.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()) != 1 {
		t.Error("the plain log event should still be forwarded as a log")
	}
}

// ── Compression ─────────────────────────────────────────────────────────────

func TestDatadogTraces_GzipBody(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true)

	jsonBody, _ := json.Marshal(sampleDDTrace())
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(jsonBody)
	_ = gz.Close()

	req := httptest.NewRequest(http.MethodPost, "/v0.4/traces", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("DD-API-KEY", "k-acme")
	rr := httptest.NewRecorder()
	p.DatadogTraces(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if f.traces == nil || len(f.traces.GetResourceSpans()) != 2 {
		t.Fatal("gzip-encoded DD traces were not decompressed/forwarded")
	}
}

// ── Native log path (Kafka → Quickwit → log explorer) ────────────────────────

// fakeLogPublisher captures the LogEntry batch that would go to Kafka.
type fakeLogPublisher struct {
	topic   string
	entries []*models.LogEntry
	err     error
}

func (f *fakeLogPublisher) PublishBatch(_ context.Context, topic string, entries []*models.LogEntry) error {
	f.topic, f.entries = topic, entries
	return f.err
}

// When a log sink is wired, DD logs must be published as native LogEntry records
// (so Quickwit indexes them into the pulsetrace-logs index the explorer reads),
// tenant-stamped and metered — not forwarded as OTLP to ClickHouse.
func TestDatadogLogs_PublishesNativeEntries(t *testing.T) {
	f := &fakeForwarder{}
	pub := &fakeLogPublisher{}
	var metered int64
	allowed := false
	p := newProxy(f, true)
	p.SetLogSink(pub,
		func(_ context.Context, _, signal string, count int64) {
			if signal == "logs" {
				metered += count
			}
		},
		func(_ context.Context, _, _ string) bool { allowed = true; return true },
	)

	body := `[{"message":"boom","ddsource":"nginx","service":"web","hostname":"h1","ddtags":"env:prod,team:core","status":"error","timestamp":1700000000000}]`
	req := httptest.NewRequest(http.MethodPost, "/api/v2/logs", strings.NewReader(body))
	req.Header.Set("DD-API-KEY", "k-acme")
	rr := httptest.NewRecorder()
	p.DatadogLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if f.logs != nil {
		t.Fatal("logs should go to Kafka (native path), not the OTLP forward, when a sink is wired")
	}
	if pub.topic != "logs" || len(pub.entries) != 1 {
		t.Fatalf("expected 1 entry on topic 'logs', got %d on %q", len(pub.entries), pub.topic)
	}
	e := pub.entries[0]
	if e.TenantID != "acme" || e.TenantTier != "premium" {
		t.Errorf("entry not tenant-stamped: %q/%q", e.TenantID, e.TenantTier)
	}
	if e.ServiceName != "web" || e.Level != models.LogLevelError || e.Message != "boom" {
		t.Errorf("entry fields wrong: svc=%q level=%q msg=%q", e.ServiceName, e.Level, e.Message)
	}
	if e.ID == "" {
		t.Error("entry ID should be generated")
	}
	if !e.Timestamp.Equal(time.Unix(0, 1700000000000*1e6).UTC()) {
		t.Errorf("entry timestamp (ms→time) wrong: %v", e.Timestamp)
	}
	meta := map[string]string{}
	_ = json.Unmarshal([]byte(e.Metadata), &meta)
	if meta["team"] != "core" || meta["datadog.source"] != "nginx" || meta["host.name"] != "h1" {
		t.Errorf("metadata not carried from ddtags/fields: %v", meta)
	}
	if !allowed || metered != 1 {
		t.Errorf("native log path must meter/quota-check: allowed=%v metered=%d", allowed, metered)
	}
}

// A quota rejection on the native log path surfaces as HTTP 429 and nothing is
// published.
func TestDatadogLogs_QuotaExceeded(t *testing.T) {
	f := &fakeForwarder{}
	pub := &fakeLogPublisher{}
	p := newProxy(f, true)
	p.SetLogSink(pub, nil, func(_ context.Context, _, _ string) bool { return false })

	body := `{"message":"boom","service":"web","status":"info"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v2/logs", strings.NewReader(body))
	req.Header.Set("DD-API-KEY", "k-acme")
	rr := httptest.NewRecorder()
	p.DatadogLogs(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
	if len(pub.entries) != 0 {
		t.Error("nothing should be published when over quota")
	}
}

// Splunk HEC log events also take the native path when a sink is wired; metric
// events still go to the metrics forward.
func TestSplunkHEC_PublishesNativeLogEntries(t *testing.T) {
	f := &fakeForwarder{}
	pub := &fakeLogPublisher{}
	p := newProxy(f, true)
	p.SetLogSink(pub, func(context.Context, string, string, int64) {}, func(context.Context, string, string) bool { return true })

	body := `{"time":1700000000,"event":"metric","fields":{"metric_name":"cpu.usage","_value":73.2,"region":"us"}}
{"event":"a normal log line","source":"app","fields":{"level":"warn"}}`
	req := httptest.NewRequest(http.MethodPost, "/services/collector", strings.NewReader(body))
	req.Header.Set("Authorization", "Splunk k-acme")
	rr := httptest.NewRecorder()
	p.SplunkHEC(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if f.metrics == nil {
		t.Fatal("metric event should still be forwarded as a metric")
	}
	if f.logs != nil {
		t.Fatal("log event should go to Kafka, not the OTLP forward")
	}
	if len(pub.entries) != 1 {
		t.Fatalf("expected 1 published log entry, got %d", len(pub.entries))
	}
	e := pub.entries[0]
	if e.ServiceName != "app" || e.Level != models.LogLevelWarning || e.Message != "a normal log line" {
		t.Errorf("splunk log entry wrong: svc=%q level=%q msg=%q", e.ServiceName, e.Level, e.Message)
	}
	if e.TenantID != "acme" {
		t.Errorf("splunk log entry not tenant-stamped: %q", e.TenantID)
	}
}
