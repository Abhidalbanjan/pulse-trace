package ingestproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// ddLog is one entry in the Datadog HTTP logs intake (POST /api/v2/logs), which
// takes a JSON array of these (or a single object).
type ddLog struct {
	Message   string      `json:"message"`
	DDSource  string      `json:"ddsource"`
	Service   string      `json:"service"`
	Hostname  string      `json:"hostname"`
	DDTags    string      `json:"ddtags"`    // comma-separated key:value
	Status    string      `json:"status"`    // info/warn/error/...
	Timestamp json.Number `json:"timestamp"` // epoch millis (optional)
}

// DatadogLogs handles POST /api/v2/logs.
func (p *Proxy) DatadogLogs(w http.ResponseWriter, r *http.Request) {
	tenantID, tier, status, ok := p.resolveTenant(r.Context(), datadogKey(r))
	if !ok {
		http.Error(w, "invalid or missing DD-API-KEY", status)
		return
	}
	body, err := readBody(r, 16<<20)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	logs, err := parseDDLogs(body)
	if err != nil {
		http.Error(w, "invalid Datadog logs payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Preferred path: publish as native LogEntry records to Kafka so the logs
	// land in the same Quickwit index the log explorer reads. Falls back to the
	// OTLP forward (→ ClickHouse otel_logs) when no log sink is wired.
	if handled, err := p.publishLogs(r.Context(), tenantID, ddLogsToEntries(logs, tenantID, tier)); handled {
		if err != nil {
			writeLogSinkError(w, err)
			return
		}
	} else if req := ddLogsToOTLP(logs); len(req.GetResourceLogs()) > 0 {
		if err := p.fwd.ForwardLogs(r.Context(), tenantID, tier, req); err != nil {
			httpForwardError(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{}`)
}

// parseDDLogs accepts either a JSON array of entries or a single entry object.
func parseDDLogs(body []byte) ([]ddLog, error) {
	trimmed := strings.TrimLeft(string(body), " \t\r\n")
	if strings.HasPrefix(trimmed, "[") {
		var logs []ddLog
		return logs, json.Unmarshal(body, &logs)
	}
	var one ddLog
	if err := json.Unmarshal(body, &one); err != nil {
		return nil, err
	}
	return []ddLog{one}, nil
}

func ddLogsToOTLP(logs []ddLog) *collogspb.ExportLogsServiceRequest {
	records := make([]*logspb.LogRecord, 0, len(logs))
	for _, l := range logs {
		rec := &logspb.LogRecord{
			TimeUnixNano:         ddLogTimeNanos(l.Timestamp),
			ObservedTimeUnixNano: uint64(time.Now().UnixNano()),
			Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: l.Message}},
			SeverityText:         l.Status,
			Attributes:           ddLogAttrs(l),
		}
		records = append(records, rec)
	}
	return &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope:      &commonpb.InstrumentationScope{Name: "pulsetrace/datadog"},
				LogRecords: records,
			}},
		}},
	}
}

func ddLogAttrs(l ddLog) []*commonpb.KeyValue {
	var attrs []*commonpb.KeyValue
	add := func(k, v string) {
		if v != "" {
			attrs = append(attrs, strAttr(k, v))
		}
	}
	add("datadog.source", l.DDSource)
	add("service.name", l.Service)
	add("host.name", l.Hostname)
	// ddtags is a comma-separated list of key:value pairs.
	for _, tag := range strings.Split(l.DDTags, ",") {
		if tag = strings.TrimSpace(tag); tag == "" {
			continue
		}
		if k, v, found := strings.Cut(tag, ":"); found {
			attrs = append(attrs, strAttr(k, v))
		}
	}
	return attrs
}

// ddLogTimeNanos converts DD's epoch-millis timestamp to nanos (0 = unset).
func ddLogTimeNanos(n json.Number) uint64 {
	if n == "" {
		return 0
	}
	ms, err := n.Int64()
	if err != nil || ms <= 0 {
		return 0
	}
	return uint64(ms) * 1e6
}
