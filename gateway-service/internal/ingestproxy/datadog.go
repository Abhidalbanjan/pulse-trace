package ingestproxy

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/vmihailenco/msgpack/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// ddSpan is one span in the Datadog trace-agent v0.3/v0.4 wire format. A payload
// is [][]ddSpan (a list of traces, each a list of spans). The same struct
// decodes both JSON and msgpack (the agent's default), via parallel tags.
type ddSpan struct {
	TraceID  uint64             `json:"trace_id" msgpack:"trace_id"`
	SpanID   uint64             `json:"span_id" msgpack:"span_id"`
	ParentID uint64             `json:"parent_id" msgpack:"parent_id"`
	Name     string             `json:"name" msgpack:"name"`         // operation name
	Resource string             `json:"resource" msgpack:"resource"` // e.g. "GET /users/:id"
	Service  string             `json:"service" msgpack:"service"`
	Type     string             `json:"type" msgpack:"type"`
	Start    int64              `json:"start" msgpack:"start"`       // unix nanos
	Duration int64              `json:"duration" msgpack:"duration"` // nanos
	Error    int32              `json:"error" msgpack:"error"`
	Meta     map[string]string  `json:"meta" msgpack:"meta"`
	Metrics  map[string]float64 `json:"metrics" msgpack:"metrics"`
}

// DatadogTraces handles the Datadog trace-agent intake (/v0.3/traces,
// /v0.4/traces). Auth is the DD-API-KEY header, which the migrating customer
// sets to their PulseTrace ingestion key.
func (p *Proxy) DatadogTraces(w http.ResponseWriter, r *http.Request) {
	tenantID, tier, status, ok := p.resolveTenant(r.Context(), datadogKey(r))
	if !ok {
		http.Error(w, "invalid or missing DD-API-KEY", status)
		return
	}

	body, err := readBody(r, 16<<20) // 16 MiB cap
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var traces [][]ddSpan
	if strings.HasSuffix(r.URL.Path, "/v0.5/traces") {
		traces, err = decodeDatadogTracesV05(body)
	} else {
		traces, err = decodeDatadogTraces(r.Header.Get("Content-Type"), body)
	}
	if err != nil {
		http.Error(w, "invalid Datadog trace payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	req := ddTracesToOTLP(traces)
	if len(req.GetResourceSpans()) > 0 {
		if err := p.fwd.ForwardTraces(r.Context(), tenantID, tier, req); err != nil {
			httpForwardError(w, err)
			return
		}
	}

	// The trace-agent expects a JSON body it can parse for sampling-rate hints;
	// an empty map means "no per-service override", which is always valid.
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"rate_by_service":{}}`)
}

// datadogKey reads the ingestion key from the DD-API-KEY header (what the DD
// agent sends), falling back to a Bearer token for flexibility.
func datadogKey(r *http.Request) string {
	if k := r.Header.Get("DD-API-KEY"); k != "" {
		return k
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// decodeDatadogTraces decodes the v0.3/v0.4 payload as msgpack (the agent
// default, or Content-Type application/msgpack) or JSON.
func decodeDatadogTraces(contentType string, body []byte) ([][]ddSpan, error) {
	var traces [][]ddSpan
	if strings.Contains(contentType, "msgpack") {
		return traces, msgpack.Unmarshal(body, &traces)
	}
	// Some libraries send JSON; a leading '[' is the JSON array of traces.
	return traces, json.Unmarshal(body, &traces)
}

// v0.5 packs traces as a 2-tuple [stringTable, traces] where every string
// (service/name/resource/type and each meta key/value) is an index into the
// table, and each span is a fixed 12-element array. This is what recent Datadog
// agents send by default, so decoding it is what makes "point your DD agent at
// us" work without the agent negotiating down to v0.4.
type ddV05Payload struct {
	_msgpack struct{} `msgpack:",as_array"`
	Strings  []string
	Traces   [][]ddSpanV05
}

type ddSpanV05 struct {
	_msgpack struct{} `msgpack:",as_array"`
	Service  uint32
	Name     uint32
	Resource uint32
	TraceID  uint64
	SpanID   uint64
	ParentID uint64
	Start    int64
	Duration int64
	Error    int32
	Meta     map[uint32]uint32
	Metrics  map[uint32]float64
	Type     uint32
}

// decodeDatadogTracesV05 resolves the string-table indices back into the
// v0.3/v0.4 ddSpan shape so the rest of the pipeline (ddTracesToOTLP) is shared.
func decodeDatadogTracesV05(body []byte) ([][]ddSpan, error) {
	var p ddV05Payload
	if err := msgpack.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	get := func(i uint32) string {
		if int(i) < len(p.Strings) {
			return p.Strings[i]
		}
		return ""
	}
	out := make([][]ddSpan, 0, len(p.Traces))
	for _, trace := range p.Traces {
		spans := make([]ddSpan, 0, len(trace))
		for _, s := range trace {
			meta := make(map[string]string, len(s.Meta))
			for k, v := range s.Meta {
				meta[get(k)] = get(v)
			}
			metrics := make(map[string]float64, len(s.Metrics))
			for k, v := range s.Metrics {
				metrics[get(k)] = v
			}
			spans = append(spans, ddSpan{
				Service: get(s.Service), Name: get(s.Name), Resource: get(s.Resource),
				TraceID: s.TraceID, SpanID: s.SpanID, ParentID: s.ParentID,
				Start: s.Start, Duration: s.Duration, Error: s.Error, Type: get(s.Type),
				Meta: meta, Metrics: metrics,
			})
		}
		out = append(out, spans)
	}
	return out, nil
}

// ddTracesToOTLP converts Datadog traces into an OTLP trace export, grouping
// spans by service (each service becomes a ResourceSpans with a service.name
// resource attribute). Pure (no I/O), unit-tested directly. The tenant Resource
// attribute is added later by ForwardTraces.
func ddTracesToOTLP(traces [][]ddSpan) *coltracepb.ExportTraceServiceRequest {
	byService := map[string][]*tracepb.Span{}
	for _, trace := range traces {
		for _, s := range trace {
			byService[s.Service] = append(byService[s.Service], ddSpanToOTLP(s))
		}
	}

	var resourceSpans []*tracepb.ResourceSpans
	for service, spans := range byService {
		res := &resourcepb.Resource{}
		if service != "" {
			res.Attributes = []*commonpb.KeyValue{strAttr("service.name", service)}
		}
		resourceSpans = append(resourceSpans, &tracepb.ResourceSpans{
			Resource: res,
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "pulsetrace/datadog"},
				Spans: spans,
			}},
		})
	}
	return &coltracepb.ExportTraceServiceRequest{ResourceSpans: resourceSpans}
}

func ddSpanToOTLP(s ddSpan) *tracepb.Span {
	// OTLP span name is conventionally the resource (the specific operation),
	// falling back to the operation name.
	name := s.Resource
	if name == "" {
		name = s.Name
	}

	span := &tracepb.Span{
		TraceId:           traceIDBytes(s.TraceID),
		SpanId:            spanIDBytes(s.SpanID),
		Name:              name,
		StartTimeUnixNano: uint64(s.Start),
		EndTimeUnixNano:   uint64(s.Start + s.Duration),
	}
	if s.ParentID != 0 {
		span.ParentSpanId = spanIDBytes(s.ParentID)
	}
	if s.Error != 0 {
		span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
	}

	// Preserve the DD operation name and type as attributes (the OTLP name took
	// the resource), then fold in meta (string) and metrics (numeric) tags.
	attrs := []*commonpb.KeyValue{}
	if s.Name != "" {
		attrs = append(attrs, strAttr("datadog.operation", s.Name))
	}
	if s.Type != "" {
		attrs = append(attrs, strAttr("datadog.type", s.Type))
	}
	for k, v := range s.Meta {
		attrs = append(attrs, strAttr(k, v))
	}
	for k, v := range s.Metrics {
		attrs = append(attrs, &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v}}})
	}
	span.Attributes = attrs
	return span
}

// traceIDBytes packs Datadog's 64-bit trace id into the low 8 bytes of a 16-byte
// OTLP trace id; spanIDBytes packs the 64-bit span id big-endian.
func traceIDBytes(id uint64) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b[8:], id)
	return b
}

func spanIDBytes(id uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, id)
	return b
}

// httpForwardError maps a forward error to an HTTP status: an over-quota
// ResourceExhausted becomes 429, anything else a 502 (the upstream collector
// failed), so a migrating agent backs off correctly.
func httpForwardError(w http.ResponseWriter, err error) {
	if status.Code(err) == codes.ResourceExhausted {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	http.Error(w, "telemetry forward failed: "+err.Error(), http.StatusBadGateway)
}
