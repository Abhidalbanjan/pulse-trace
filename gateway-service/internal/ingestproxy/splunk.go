package ingestproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// hecEvent is one Splunk HTTP Event Collector event. A HEC request body is one
// or more of these concatenated (not a JSON array), so we stream-decode them.
type hecEvent struct {
	Time       json.Number     `json:"time"`
	Host       string          `json:"host"`
	Source     string          `json:"source"`
	Sourcetype string          `json:"sourcetype"`
	Index      string          `json:"index"`
	Event      json.RawMessage `json:"event"`
	Fields     map[string]any  `json:"fields"`
}

// SplunkHEC handles the JSON event endpoints (/services/collector[/event]).
// Auth is the Splunk token — "Authorization: Splunk <token>" — which the
// migrating customer sets to their PulseTrace ingestion key.
func (p *Proxy) SplunkHEC(w http.ResponseWriter, r *http.Request) {
	tenantID, tier, ok := p.authSplunk(w, r)
	if !ok {
		return
	}

	body, err := readBody(r, 8<<20) // 8 MiB cap, decompresses gzip
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var events []hecEvent
	dec := json.NewDecoder(strings.NewReader(string(body)))
	for {
		var e hecEvent
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			http.Error(w, "invalid HEC payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		events = append(events, e)
	}

	if len(events) == 0 {
		writeHECResponse(w, http.StatusOK)
		return
	}

	// HEC carries both logs and metrics on the same endpoint; a metric event is
	// one whose fields declare a metric_name. Route each kind to its own OTLP
	// signal so Splunk metrics land in the metrics pillar, not as log lines.
	logEvents, metricEvents := partitionHEC(events)

	if len(metricEvents) > 0 {
		mreq := normMetricsToOTLP(hecEventsToMetrics(metricEvents))
		if err := p.fwd.ForwardMetrics(r.Context(), tenantID, tier, mreq); err != nil {
			httpForwardError(w, err)
			return
		}
	}
	if len(logEvents) > 0 {
		lreq := hecEventsToOTLP(logEvents)
		if err := p.fwd.ForwardLogs(r.Context(), tenantID, tier, lreq); err != nil {
			httpForwardError(w, err)
			return
		}
	}
	writeHECResponse(w, http.StatusOK)
}

// partitionHEC splits events into (logs, metrics). A metric event is identified
// by a metric_name field (single-metric format) or any metric_name:<name> field
// (multi-metric format) — Splunk's HEC metrics conventions.
func partitionHEC(events []hecEvent) (logs, metrics []hecEvent) {
	for _, e := range events {
		if hecIsMetric(e) {
			metrics = append(metrics, e)
		} else {
			logs = append(logs, e)
		}
	}
	return logs, metrics
}

func hecIsMetric(e hecEvent) bool {
	if _, ok := e.Fields["metric_name"]; ok {
		return true
	}
	for k := range e.Fields {
		if strings.HasPrefix(k, "metric_name:") {
			return true
		}
	}
	return false
}

// hecEventsToMetrics converts HEC metric events into the version-agnostic
// normMetric form (reusing the Datadog metrics OTLP translator). Both the
// single-metric (metric_name + _value) and multi-metric (metric_name:<n>=v)
// layouts are supported; every other field becomes a dimension attribute.
func hecEventsToMetrics(events []hecEvent) []normMetric {
	var out []normMetric
	for _, e := range events {
		ts := hecTimeNanos(e.Time)
		dims := hecMetricDimensions(e)

		// Single-metric: {"metric_name":"cpu.usage","_value":42}
		if name, ok := e.Fields["metric_name"].(string); ok && name != "" {
			out = append(out, normMetric{
				name:   name,
				kind:   "gauge",
				attrs:  dims,
				points: []normPoint{{tsNanos: ts, value: toFloat(e.Fields["_value"])}},
			})
		}
		// Multi-metric: {"metric_name:cpu.usage":42,"metric_name:mem":128}
		for k, v := range e.Fields {
			if name, found := strings.CutPrefix(k, "metric_name:"); found && name != "" {
				out = append(out, normMetric{
					name:   name,
					kind:   "gauge",
					attrs:  dims,
					points: []normPoint{{tsNanos: ts, value: toFloat(v)}},
				})
			}
		}
	}
	return out
}

// hecMetricDimensions collects everything that isn't a metric name/value into
// OTLP attributes, plus the standard HEC metadata.
func hecMetricDimensions(e hecEvent) []*commonpb.KeyValue {
	var attrs []*commonpb.KeyValue
	add := func(k, v string) {
		if v != "" {
			attrs = append(attrs, strAttr(k, v))
		}
	}
	add("splunk.host", e.Host)
	add("splunk.source", e.Source)
	add("splunk.index", e.Index)
	for k, v := range e.Fields {
		if k == "metric_name" || k == "_value" || strings.HasPrefix(k, "metric_name:") {
			continue
		}
		attrs = append(attrs, strAttr(k, fmt.Sprintf("%v", v)))
	}
	return attrs
}

// toFloat coerces a decoded JSON value (float64, json.Number, or numeric string)
// to float64; anything else yields 0.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}

// SplunkHECRaw handles /services/collector/raw, where the whole body is a single
// event and metadata comes from query params.
func (p *Proxy) SplunkHECRaw(w http.ResponseWriter, r *http.Request) {
	tenantID, tier, ok := p.authSplunk(w, r)
	if !ok {
		return
	}
	body, err := readBody(r, 8<<20)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	e := hecEvent{
		Host:       q.Get("host"),
		Source:     q.Get("source"),
		Sourcetype: q.Get("sourcetype"),
		Index:      q.Get("index"),
		Event:      json.RawMessage(strconv.Quote(string(body))), // treat raw body as a string event
	}
	req := hecEventsToOTLP([]hecEvent{e})
	if err := p.fwd.ForwardLogs(r.Context(), tenantID, tier, req); err != nil {
		httpForwardError(w, err)
		return
	}
	writeHECResponse(w, http.StatusOK)
}

// authSplunk resolves the tenant from the Splunk token and writes the error
// response itself on failure (returning ok=false).
func (p *Proxy) authSplunk(w http.ResponseWriter, r *http.Request) (tenantID, tier string, ok bool) {
	token := splunkToken(r)
	tid, tr, status, ok := p.resolveTenant(r.Context(), token)
	if !ok {
		// HEC clients expect a JSON error envelope, not a bare string.
		writeHECError(w, status)
		return "", "", false
	}
	return tid, tr, true
}

// splunkToken pulls the token from "Authorization: Splunk <token>" (Splunk's
// scheme), tolerating a bare "Bearer"/token too for flexibility.
func splunkToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	for _, prefix := range []string{"Splunk ", "Bearer "} {
		if strings.HasPrefix(h, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(h, prefix))
		}
	}
	return strings.TrimSpace(h)
}

// hecEventsToOTLP converts HEC events into a single OTLP logs export. Pure (no
// I/O) so it's unit-tested directly. The tenant Resource is stamped later by
// ForwardLogs, so the resource is left empty here.
func hecEventsToOTLP(events []hecEvent) *collogspb.ExportLogsServiceRequest {
	records := make([]*logspb.LogRecord, 0, len(events))
	for _, e := range events {
		rec := &logspb.LogRecord{
			TimeUnixNano:         hecTimeNanos(e.Time),
			ObservedTimeUnixNano: uint64(time.Now().UnixNano()),
			Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: hecBody(e.Event)}},
			Attributes:           hecAttributes(e),
		}
		records = append(records, rec)
	}
	return &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope:      &commonpb.InstrumentationScope{Name: "pulsetrace/splunk-hec"},
				LogRecords: records,
			}},
		}},
	}
}

// hecTimeNanos converts HEC epoch-seconds (int or fractional) to nanos, or 0
// (meaning "unset", the collector/consumer falls back to observed time).
func hecTimeNanos(n json.Number) uint64 {
	if n == "" {
		return 0
	}
	secs, err := n.Float64()
	if err != nil || secs <= 0 {
		return 0
	}
	return uint64(secs * 1e9)
}

// hecBody renders the event payload as a string: a JSON string is unquoted, any
// other JSON (object/number/array) is kept as its compact JSON text.
func hecBody(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	return string(raw)
}

// hecAttributes maps the HEC metadata + custom fields onto OTLP log attributes.
func hecAttributes(e hecEvent) []*commonpb.KeyValue {
	var attrs []*commonpb.KeyValue
	add := func(k, v string) {
		if v != "" {
			attrs = append(attrs, strAttr(k, v))
		}
	}
	add("splunk.host", e.Host)
	add("splunk.source", e.Source)
	add("splunk.sourcetype", e.Sourcetype)
	add("splunk.index", e.Index)
	for k, v := range e.Fields {
		attrs = append(attrs, strAttr("fields."+k, fmt.Sprintf("%v", v)))
	}
	return attrs
}

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func writeHECResponse(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"text":"Success","code":0}`)
}

func writeHECError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// code 4 is Splunk HEC's "invalid authorization".
	_, _ = io.WriteString(w, `{"text":"Invalid authorization","code":4}`)
}
