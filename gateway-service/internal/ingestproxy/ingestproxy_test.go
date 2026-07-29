package ingestproxy

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vmihailenco/msgpack/v5"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// fakeForwarder captures what would be forwarded, so a full request can be
// driven through the handler and asserted on without a real collector.
type fakeForwarder struct {
	tenantID, tier string
	traces         *coltracepb.ExportTraceServiceRequest
	logs           *collogspb.ExportLogsServiceRequest
	err            error
}

func (f *fakeForwarder) ForwardTraces(_ context.Context, tenantID, tier string, req *coltracepb.ExportTraceServiceRequest) error {
	f.tenantID, f.tier, f.traces = tenantID, tier, req
	return f.err
}
func (f *fakeForwarder) ForwardLogs(_ context.Context, tenantID, tier string, req *collogspb.ExportLogsServiceRequest) error {
	f.tenantID, f.tier, f.logs = tenantID, tier, req
	return f.err
}

// fakeResolver maps a fixed key → tenant/scope.
type fakeResolver struct {
	key, tenant, tier, scope string
}

func (r fakeResolver) Resolve(_ context.Context, plaintext string) (string, string, string, bool) {
	if plaintext == r.key && plaintext != "" {
		return r.tenant, r.tier, r.scope, true
	}
	return "", "", "", false
}

func newProxy(f *fakeForwarder, requireKey bool) *Proxy {
	return New(f, fakeResolver{key: "k-acme", tenant: "acme", tier: "premium", scope: "ingest"}, requireKey)
}

// ── Splunk ────────────────────────────────────────────────────────────────

func TestSplunkHEC_TranslatesAndAuthenticates(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true)

	body := `{"time":1700000000,"host":"web01","source":"nginx","sourcetype":"access","event":"GET /health 200","fields":{"env":"prod"}}
{"event":{"msg":"structured"},"index":"main"}`
	req := httptest.NewRequest(http.MethodPost, "/services/collector", strings.NewReader(body))
	req.Header.Set("Authorization", "Splunk k-acme")
	rr := httptest.NewRecorder()
	p.SplunkHEC(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if f.tenantID != "acme" || f.tier != "premium" {
		t.Fatalf("tenant not resolved from Splunk token: %q/%q", f.tenantID, f.tier)
	}
	recs := f.logs.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()
	if len(recs) != 2 {
		t.Fatalf("expected 2 log records, got %d", len(recs))
	}
	if got := recs[0].GetBody().GetStringValue(); got != "GET /health 200" {
		t.Errorf("string event body = %q", got)
	}
	if recs[0].GetTimeUnixNano() != 1700000000*1e9 {
		t.Errorf("time not converted to nanos: %d", recs[0].GetTimeUnixNano())
	}
	// A structured (object) event is preserved as its JSON text.
	if got := recs[1].GetBody().GetStringValue(); !strings.Contains(got, "structured") {
		t.Errorf("object event body = %q", got)
	}
}

func TestSplunkHEC_RejectsBadToken(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true) // requireKey

	req := httptest.NewRequest(http.MethodPost, "/services/collector", strings.NewReader(`{"event":"x"}`))
	req.Header.Set("Authorization", "Splunk wrong-key")
	rr := httptest.NewRecorder()
	p.SplunkHEC(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad token should be 401, got %d", rr.Code)
	}
	if f.logs != nil {
		t.Fatal("nothing should be forwarded on an auth failure")
	}
}

func TestSplunkHEC_RejectsRumScopedKey(t *testing.T) {
	f := &fakeForwarder{}
	// A public RUM-scoped key must never write server telemetry.
	p := New(f, fakeResolver{key: "k-rum", tenant: "acme", tier: "std", scope: "rum"}, true)

	req := httptest.NewRequest(http.MethodPost, "/services/collector", strings.NewReader(`{"event":"x"}`))
	req.Header.Set("Authorization", "Splunk k-rum")
	rr := httptest.NewRecorder()
	p.SplunkHEC(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("RUM-scoped key should be 403, got %d", rr.Code)
	}
}

func TestSplunkHEC_DefaultTenantWhenKeyNotRequired(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, false) // requireKey=false

	req := httptest.NewRequest(http.MethodPost, "/services/collector", strings.NewReader(`{"event":"x"}`))
	// No auth header at all.
	rr := httptest.NewRecorder()
	p.SplunkHEC(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if f.tenantID != "default" {
		t.Fatalf("unauthenticated ingest should fall back to default tenant, got %q", f.tenantID)
	}
}

// ── Datadog ───────────────────────────────────────────────────────────────

func sampleDDTrace() [][]ddSpan {
	return [][]ddSpan{{
		{TraceID: 111, SpanID: 222, Name: "http.request", Resource: "GET /users", Service: "api",
			Type: "web", Start: 1_700_000_000_000_000_000, Duration: 5_000_000, Error: 1,
			Meta: map[string]string{"http.method": "GET"}, Metrics: map[string]float64{"_sampling_priority_v1": 1}},
		{TraceID: 111, SpanID: 333, ParentID: 222, Name: "pg.query", Resource: "SELECT users", Service: "postgres",
			Start: 1_700_000_000_001_000_000, Duration: 2_000_000},
	}}
}

func TestDDTracesToOTLP_Mapping(t *testing.T) {
	req := ddTracesToOTLP(sampleDDTrace())

	// Two services → two ResourceSpans.
	if len(req.GetResourceSpans()) != 2 {
		t.Fatalf("expected 2 resource spans (one per service), got %d", len(req.GetResourceSpans()))
	}

	var apiSpan, pgSpan *tracepb.Span
	for _, rs := range req.GetResourceSpans() {
		svc := rs.GetResource().GetAttributes()[0].GetValue().GetStringValue()
		sp := rs.GetScopeSpans()[0].GetSpans()[0]
		if svc == "api" {
			apiSpan = sp
		} else if svc == "postgres" {
			pgSpan = sp
		}
	}
	if apiSpan == nil || pgSpan == nil {
		t.Fatal("spans not grouped by service.name")
	}
	// OTLP span name is the DD resource; the DD operation name is kept as an attr.
	if apiSpan.GetName() != "GET /users" {
		t.Errorf("span name = %q, want the DD resource", apiSpan.GetName())
	}
	// Error span → OTLP ERROR status.
	if apiSpan.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
		t.Errorf("error span should map to STATUS_CODE_ERROR")
	}
	// End = start + duration.
	if apiSpan.GetEndTimeUnixNano() != apiSpan.GetStartTimeUnixNano()+5_000_000 {
		t.Errorf("end time not start+duration")
	}
	// 64-bit DD trace id lands in the low 8 bytes of the 16-byte OTLP id.
	if got := binary.BigEndian.Uint64(apiSpan.GetTraceId()[8:]); got != 111 {
		t.Errorf("trace id low-64 = %d, want 111", got)
	}
	// Child span carries its parent.
	if binary.BigEndian.Uint64(pgSpan.GetParentSpanId()) != 222 {
		t.Errorf("parent span id not preserved")
	}
}

func TestDatadogTraces_MsgpackEndToEnd(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true)

	packed, err := msgpack.Marshal(sampleDDTrace())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v0.4/traces", strings.NewReader(string(packed)))
	req.Header.Set("Content-Type", "application/msgpack")
	req.Header.Set("DD-API-KEY", "k-acme")
	rr := httptest.NewRecorder()
	p.DatadogTraces(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	// Agent expects a JSON sampling-rate body it can parse.
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v (%s)", err, rr.Body.String())
	}
	if f.tenantID != "acme" {
		t.Fatalf("tenant not resolved from DD-API-KEY, got %q", f.tenantID)
	}
	if f.traces == nil || len(f.traces.GetResourceSpans()) != 2 {
		t.Fatal("msgpack traces not decoded/forwarded")
	}
}

func TestDatadogTraces_JSONEndToEnd(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true)

	jsonBody, _ := json.Marshal(sampleDDTrace())
	req := httptest.NewRequest(http.MethodPost, "/v0.3/traces", strings.NewReader(string(jsonBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("DD-API-KEY", "k-acme")
	rr := httptest.NewRecorder()
	p.DatadogTraces(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if f.traces == nil || len(f.traces.GetResourceSpans()) != 2 {
		t.Fatal("JSON traces not decoded/forwarded")
	}
}

func TestDatadogTraces_RejectsMissingKey(t *testing.T) {
	f := &fakeForwarder{}
	p := newProxy(f, true) // requireKey

	jsonBody, _ := json.Marshal(sampleDDTrace())
	req := httptest.NewRequest(http.MethodPost, "/v0.4/traces", strings.NewReader(string(jsonBody)))
	req.Header.Set("Content-Type", "application/json")
	// No DD-API-KEY.
	rr := httptest.NewRecorder()
	p.DatadogTraces(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing key should be 401, got %d", rr.Code)
	}
	if f.traces != nil {
		t.Fatal("nothing should be forwarded without a key")
	}
}
