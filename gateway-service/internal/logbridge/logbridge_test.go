package logbridge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/pulsetrace/shared/models"
)

type fakePub struct {
	topic   string
	entries []*models.LogEntry
	err     error
}

func (f *fakePub) PublishBatch(_ context.Context, topic string, entries []*models.LogEntry) error {
	f.topic, f.entries = topic, entries
	return f.err
}

func strVal(s string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
}

func TestBridgePublish_ConvertsOTLPLogs(t *testing.T) {
	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				{Key: "service.name", Value: strVal("checkout")},
			}},
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano:  uint64(time.Unix(1_700_000_000, 0).UnixNano()),
					SeverityText:  "Error",
					Body:          strVal("db connection failed"),
					TraceId:       traceID,
					SpanId:        spanID,
					Attributes:    []*commonpb.KeyValue{{Key: "db.system", Value: strVal("postgres")}},
				}},
			}},
		}},
	}

	pub := &fakePub{}
	b := New(pub)
	if err := b.Publish(context.Background(), "acme", "premium", req); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.topic != "logs" || len(pub.entries) != 1 {
		t.Fatalf("expected 1 entry on 'logs', got %d on %q", len(pub.entries), pub.topic)
	}
	e := pub.entries[0]
	if e.TenantID != "acme" || e.TenantTier != "premium" {
		t.Errorf("tenant not carried: %q/%q", e.TenantID, e.TenantTier)
	}
	if e.ServiceName != "checkout" {
		t.Errorf("service.name = %q, want checkout", e.ServiceName)
	}
	if e.Level != models.LogLevelError {
		t.Errorf("level = %q, want ERROR", e.Level)
	}
	if e.Message != "db connection failed" {
		t.Errorf("message = %q", e.Message)
	}
	if e.TraceID != hex.EncodeToString(traceID) || e.SpanID != hex.EncodeToString(spanID) {
		t.Errorf("trace/span id not hex-encoded: %q/%q", e.TraceID, e.SpanID)
	}
	if !e.Timestamp.Equal(time.Unix(1_700_000_000, 0).UTC()) {
		t.Errorf("timestamp = %v", e.Timestamp)
	}
	meta := map[string]string{}
	_ = json.Unmarshal([]byte(e.Metadata), &meta)
	if meta["db.system"] != "postgres" {
		t.Errorf("record attribute not carried into metadata: %v", meta)
	}
}

func TestBridgePublish_EmptyIsNoop(t *testing.T) {
	pub := &fakePub{}
	if err := New(pub).Publish(context.Background(), "acme", "standard", &collogspb.ExportLogsServiceRequest{}); err != nil {
		t.Fatalf("empty publish should be a no-op, got %v", err)
	}
	if pub.entries != nil {
		t.Error("nothing should be published for an empty export")
	}
}

func TestSeverityToLevel(t *testing.T) {
	cases := []struct {
		text string
		num  logspb.SeverityNumber
		want models.LogLevel
	}{
		{"", logspb.SeverityNumber_SEVERITY_NUMBER_INFO, models.LogLevelInfo},
		{"", logspb.SeverityNumber_SEVERITY_NUMBER_ERROR2, models.LogLevelError},
		{"", logspb.SeverityNumber_SEVERITY_NUMBER_FATAL, models.LogLevelFatal},
		{"", logspb.SeverityNumber_SEVERITY_NUMBER_WARN3, models.LogLevelWarning},
		{"", logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG, models.LogLevelDebug},
		{"WARN", logspb.SeverityNumber_SEVERITY_NUMBER_INFO, models.LogLevelWarning}, // text wins
		{"critical", logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED, models.LogLevelFatal},
		{"", logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED, models.LogLevelInfo},
	}
	for _, c := range cases {
		if got := severityToLevel(c.text, c.num); got != c.want {
			t.Errorf("severityToLevel(%q,%v) = %q, want %q", c.text, c.num, got, c.want)
		}
	}
}

func TestBytesToHex_UnsetIsEmpty(t *testing.T) {
	if got := bytesToHex(make([]byte, 16)); got != "" {
		t.Errorf("all-zero id should be empty, got %q", got)
	}
	if got := bytesToHex(nil); got != "" {
		t.Errorf("nil id should be empty, got %q", got)
	}
}
