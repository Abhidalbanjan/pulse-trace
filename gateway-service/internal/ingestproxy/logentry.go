package ingestproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pulsetrace/shared/models"
)

// logsTopic is the Kafka topic Quickwit's native source indexes into the
// pulsetrace-logs index that the log explorer UI reads. It MUST match
// log-service's handler.logsTopic ("logs") and quickwit/kafka-source.yaml.
//
// Migration logs (Datadog /api/v2/logs, Splunk HEC) are published here as
// models.LogEntry — the exact shape log-service produces — so they flow through
// the identical native path (Kafka → Quickwit) and show up in the same log
// explorer as native logs, tenant-scoped by tenant_id. Without this they only
// reached ClickHouse otel_logs (via the OTLP forward), which the explorer never
// queries, so migrated customers couldn't see their logs in the product UI.
const logsTopic = "logs"

// LogPublisher publishes a batch of native LogEntry records to Kafka (satisfied
// by *kafka.Producer). Injected so this package stays decoupled from Kafka and
// unit-testable; when it's nil the log handlers fall back to the OTLP forward.
type LogPublisher interface {
	PublishBatch(ctx context.Context, topic string, entries []*models.LogEntry) error
}

// MeterFunc / QuotaFunc mirror otlp.RecordFunc / otlp.AllowFunc. The OTLP forward
// path meters and quota-checks inside ForwardTraces/ForwardMetrics/ForwardLogs;
// when logs are diverted to the Kafka path instead, they bypass that path, so the
// same accounting is applied here to keep per-tenant metering and quotas intact.
type MeterFunc func(ctx context.Context, tenantID, signal string, count int64)

// QuotaFunc reports whether the tenant is under quota for a signal.
type QuotaFunc func(ctx context.Context, tenantID, signal string) bool

// ddLogsToEntries converts Datadog intake logs into native LogEntry records,
// stamped with the resolved tenant. Pure (no I/O) so it's unit-tested directly.
func ddLogsToEntries(logs []ddLog, tenantID, tier string) []*models.LogEntry {
	out := make([]*models.LogEntry, 0, len(logs))
	for _, l := range logs {
		meta := map[string]string{}
		putNonEmpty(meta, "datadog.source", l.DDSource)
		putNonEmpty(meta, "host.name", l.Hostname)
		// ddtags is a comma-separated list of key:value pairs.
		for _, tag := range strings.Split(l.DDTags, ",") {
			if tag = strings.TrimSpace(tag); tag == "" {
				continue
			}
			if k, v, found := strings.Cut(tag, ":"); found {
				meta[k] = v
			}
		}
		out = append(out, &models.LogEntry{
			TenantID:    tenantID,
			TenantTier:  tier,
			ID:          uuid.NewString(),
			ServiceName: firstNonEmpty(l.Service, l.DDSource, "datadog"),
			Level:       normalizeLevel(l.Status),
			Message:     l.Message,
			Metadata:    encodeMeta(meta),
			Timestamp:   nanosToTime(ddLogTimeNanos(l.Timestamp)),
			CreatedAt:   time.Now().UTC(),
		})
	}
	return out
}

// hecLogsToEntries converts Splunk HEC log events into native LogEntry records.
func hecLogsToEntries(events []hecEvent, tenantID, tier string) []*models.LogEntry {
	out := make([]*models.LogEntry, 0, len(events))
	for _, e := range events {
		meta := map[string]string{}
		putNonEmpty(meta, "splunk.host", e.Host)
		putNonEmpty(meta, "splunk.source", e.Source)
		putNonEmpty(meta, "splunk.sourcetype", e.Sourcetype)
		putNonEmpty(meta, "splunk.index", e.Index)
		for k, v := range e.Fields {
			meta[k] = fmt.Sprintf("%v", v)
		}
		level := normalizeLevel(firstNonEmpty(
			fieldString(e.Fields, "level"),
			fieldString(e.Fields, "severity"),
			fieldString(e.Fields, "log_level"),
		))
		out = append(out, &models.LogEntry{
			TenantID:    tenantID,
			TenantTier:  tier,
			ID:          uuid.NewString(),
			ServiceName: firstNonEmpty(e.Source, e.Sourcetype, fieldString(e.Fields, "service"), "splunk"),
			Level:       level,
			Message:     hecBody(e.Event),
			Metadata:    encodeMeta(meta),
			Timestamp:   nanosToTime(hecTimeNanos(e.Time)),
			CreatedAt:   time.Now().UTC(),
		})
	}
	return out
}

// normalizeLevel maps an arbitrary Datadog/Splunk status/severity string onto the
// product's LogLevel enum (what the explorer's level filter and aggregations
// expect). Unknown/absent → INFO.
func normalizeLevel(s string) models.LogLevel {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG", "TRACE", "FINE":
		return models.LogLevelDebug
	case "INFO", "INFORMATIONAL", "NOTICE", "":
		return models.LogLevelInfo
	case "WARN", "WARNING":
		return models.LogLevelWarning
	case "ERR", "ERROR", "SEVERE":
		return models.LogLevelError
	case "CRIT", "CRITICAL", "FATAL", "ALERT", "EMERG", "EMERGENCY", "PANIC":
		return models.LogLevelFatal
	default:
		return models.LogLevelInfo
	}
}

// nanosToTime converts a wire timestamp (nanos since epoch, 0 = unset) to a UTC
// time, defaulting to now when unset — matching log-service's default.
func nanosToTime(nanos uint64) time.Time {
	if nanos == 0 {
		return time.Now().UTC()
	}
	return time.Unix(0, int64(nanos)).UTC()
}

// encodeMeta renders the extra-fields map as the JSON blob LogEntry.Metadata
// carries (empty string when there's nothing).
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

func putNonEmpty(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// fieldString reads a HEC custom field as a string (coercing non-string scalars),
// or "" if absent.
func fieldString(fields map[string]any, key string) string {
	v, ok := fields[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
