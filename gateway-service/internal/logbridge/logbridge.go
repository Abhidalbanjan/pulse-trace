// Package logbridge converts tenant-stamped OTLP log records into native
// LogEntry records and publishes them to the same Kafka topic (→ Quickwit →
// pulsetrace-logs index) that the log explorer UI reads.
//
// OTLP-native logs would otherwise reach only ClickHouse otel_logs (via the
// collector), which nothing queries — so they were invisible in the product's log
// explorer. Routing them through this bridge unifies every log source (native app
// logs via Vector→log-service, Datadog/Splunk migration logs via the ingestproxy,
// and OTLP-native logs via the gateway's OTLP receiver) into one queryable store.
//
// It's injected into the OTLP receiver + HTTP handler as a LogSinkFunc, so those
// paths publish here instead of forwarding logs to the collector. Metering and
// quota are already applied by the OTLP receiver before the sink runs, so the
// bridge only converts and publishes.
package logbridge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/pulsetrace/shared/models"
)

// logsTopic must match log-service's handler.logsTopic and the ingestproxy — the
// topic Quickwit's native source indexes into pulsetrace-logs.
const logsTopic = "logs"

// LogPublisher publishes a LogEntry batch to the message bus (satisfied by
// bus.Bus).
type LogPublisher interface {
	PublishBatch(ctx context.Context, topic string, entries []*models.LogEntry) error
}

// Bridge converts OTLP log exports to LogEntry records and publishes them.
type Bridge struct {
	pub LogPublisher
}

func New(pub LogPublisher) *Bridge { return &Bridge{pub: pub} }

// Publish satisfies otlp.LogSinkFunc: it converts an already-tenant-stamped OTLP
// log export into LogEntry records and publishes them to Kafka. tenantID/tier are
// the resolved tenant (the receiver stamps them onto the resource and passes them
// here) so they win over anything in the payload.
func (b *Bridge) Publish(ctx context.Context, tenantID, tier string, req *collogspb.ExportLogsServiceRequest) error {
	entries := otlpLogsToEntries(req, tenantID, tier)
	if len(entries) == 0 {
		return nil
	}
	return b.pub.PublishBatch(ctx, logsTopic, entries)
}

func otlpLogsToEntries(req *collogspb.ExportLogsServiceRequest, tenantID, tier string) []*models.LogEntry {
	var out []*models.LogEntry
	for _, rl := range req.GetResourceLogs() {
		resAttrs := attrsToMap(rl.GetResource().GetAttributes())
		service := firstNonEmpty(resAttrs["service.name"], "otlp")
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				out = append(out, recordToEntry(rec, service, resAttrs, tenantID, tier))
			}
		}
	}
	return out
}

func recordToEntry(rec *logspb.LogRecord, service string, resAttrs map[string]string, tenantID, tier string) *models.LogEntry {
	// Log-record attributes become the metadata blob; nothing overwrites the
	// resource-level service/tenant we already resolved.
	meta := attrsToMap(rec.GetAttributes())
	if sev := rec.GetSeverityNumber(); sev != 0 {
		meta["severity.number"] = strconv.Itoa(int(sev))
	}

	ts := rec.GetTimeUnixNano()
	if ts == 0 {
		ts = rec.GetObservedTimeUnixNano()
	}
	var when time.Time
	if ts == 0 {
		when = time.Now().UTC()
	} else {
		when = time.Unix(0, int64(ts)).UTC()
	}

	return &models.LogEntry{
		TenantID:    tenantID,
		TenantTier:  tier,
		ID:          uuid.NewString(),
		ServiceName: service,
		Level:       severityToLevel(rec.GetSeverityText(), rec.GetSeverityNumber()),
		Message:     anyValueToString(rec.GetBody()),
		TraceID:     bytesToHex(rec.GetTraceId()),
		SpanID:      bytesToHex(rec.GetSpanId()),
		Metadata:    encodeMeta(meta),
		Timestamp:   when,
		CreatedAt:   time.Now().UTC(),
	}
}

// severityToLevel maps OTLP severity to the product's LogLevel. SeverityText wins
// when present; otherwise the numeric range (1-24) is bucketed.
func severityToLevel(text string, num logspb.SeverityNumber) models.LogLevel {
	switch normalizeSeverityText(text) {
	case "TRACE", "DEBUG":
		return models.LogLevelDebug
	case "INFO":
		return models.LogLevelInfo
	case "WARN", "WARNING":
		return models.LogLevelWarning
	case "ERROR", "ERR":
		return models.LogLevelError
	case "FATAL", "CRITICAL":
		return models.LogLevelFatal
	}
	switch {
	case num >= logspb.SeverityNumber_SEVERITY_NUMBER_FATAL:
		return models.LogLevelFatal
	case num >= logspb.SeverityNumber_SEVERITY_NUMBER_ERROR:
		return models.LogLevelError
	case num >= logspb.SeverityNumber_SEVERITY_NUMBER_WARN:
		return models.LogLevelWarning
	case num >= logspb.SeverityNumber_SEVERITY_NUMBER_INFO:
		return models.LogLevelInfo
	case num >= logspb.SeverityNumber_SEVERITY_NUMBER_TRACE:
		return models.LogLevelDebug
	default:
		return models.LogLevelInfo
	}
}

func normalizeSeverityText(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

// anyValueToString renders an OTLP AnyValue body as a log message string.
func anyValueToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch val := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(val.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(val.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(val.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_BytesValue:
		return string(val.BytesValue)
	case *commonpb.AnyValue_KvlistValue, *commonpb.AnyValue_ArrayValue:
		// Structured body → compact JSON of its flattened form.
		b, err := json.Marshal(anyValueToNative(v))
		if err == nil {
			return string(b)
		}
	}
	return ""
}

// anyValueToNative converts an AnyValue into a plain Go value for JSON encoding.
func anyValueToNative(v *commonpb.AnyValue) any {
	switch val := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_BoolValue:
		return val.BoolValue
	case *commonpb.AnyValue_IntValue:
		return val.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return val.DoubleValue
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(val.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		arr := val.ArrayValue.GetValues()
		out := make([]any, len(arr))
		for i, e := range arr {
			out[i] = anyValueToNative(e)
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		out := map[string]any{}
		for _, kv := range val.KvlistValue.GetValues() {
			out[kv.GetKey()] = anyValueToNative(kv.GetValue())
		}
		return out
	default:
		return nil
	}
}

// attrsToMap flattens OTLP key-values to a string map (scalar-stringified).
func attrsToMap(attrs []*commonpb.KeyValue) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		m[kv.GetKey()] = anyValueScalarString(kv.GetValue())
	}
	return m
}

func anyValueScalarString(v *commonpb.AnyValue) string {
	switch v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue, *commonpb.AnyValue_BoolValue,
		*commonpb.AnyValue_IntValue, *commonpb.AnyValue_DoubleValue,
		*commonpb.AnyValue_BytesValue:
		return anyValueToString(v)
	default:
		return fmt.Sprintf("%v", anyValueToNative(v))
	}
}

func bytesToHex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	// An all-zero trace/span id means "unset" in OTLP.
	for _, c := range b {
		if c != 0 {
			return hex.EncodeToString(b)
		}
	}
	return ""
}

func encodeMeta(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
