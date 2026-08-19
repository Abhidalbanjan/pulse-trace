package sqlq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ClickHouse-backed relations.
//
// Every scanner in this file builds its statement from a compile-time constant
// plus a bind parameter. Nothing derived from the user's query is in scope here
// — the Scanner interface does not carry it — so the only value that varies is
// the tenant, and it travels as `param_tenant` rather than as text. That is the
// same mechanism the existing handlers use; what changes is that here it is the
// *only* mechanism, because there is no server-authored SQL to get right.

// chColumn maps one catalog column to a ClickHouse expression.
//
// The mapping is explicit and one-directional: a catalog column names a physical
// expression, never the reverse. A column added to a ClickHouse table therefore
// cannot appear in user SQL until someone adds it here and to the catalog —
// which matters, because these tables carry columns users must not see
// (TenantID itself, ResourceAttributes) and reflection would have exposed them.
type chColumn struct {
	logical string
	expr    string
}

// chTable is the physical binding for one catalog relation.
type chTable struct {
	table string
	// tenantPred is the WHERE fragment isolating one tenant. It differs per
	// table and that difference is the reason this is data rather than a shared
	// helper: otel_traces has no TenantID column at all — the collector writes
	// the tenant into ResourceAttributes — while the app-owned tables have a
	// real column. A single "add WHERE TenantID = ..." helper would have been
	// silently wrong for traces, which is the largest table of the three.
	tenantPred string
	columns    []chColumn
}

var chTables = map[string]chTable{
	"traces": {
		table:      "pulsetrace.otel_traces",
		tenantPred: "ResourceAttributes['tenant.id'] = {tenant:String}",
		columns: []chColumn{
			{"timestamp", "Timestamp"},
			{"trace_id", "TraceId"},
			{"span_id", "SpanId"},
			{"parent_span_id", "ParentSpanId"},
			{"service_name", "ServiceName"},
			{"span_name", "SpanName"},
			{"span_kind", "SpanKind"},
			// Duration is nanoseconds in the OTel schema; the catalog advertises
			// milliseconds, so the conversion belongs here rather than in every
			// user's query.
			{"duration_ms", "Duration / 1000000"},
			{"status_code", "StatusCode"},
			{"status_message", "StatusMessage"},
		},
	},
	"rum_events": {
		table:      "pulsetrace.rum_events",
		tenantPred: "TenantID = {tenant:String}",
		columns: []chColumn{
			{"timestamp", "Timestamp"},
			{"session_id", "SessionID"},
			{"event_type", "Type"},
			{"path", "Path"},
			{"user_agent", "UserAgent"},
			{"metric_name", "MetricName"},
			{"metric_value", "MetricValue"},
			{"error_message", "ErrorMsg"},
			{"trace_id", "TraceID"},
			{"span_id", "SpanID"},
		},
	},
	"synthetic_results": {
		table:      "pulsetrace.synthetic_results",
		tenantPred: "TenantID = {tenant:String}",
		columns: []chColumn{
			{"timestamp", "Timestamp"},
			{"check_name", "CheckName"},
			{"url", "URL"},
			{"status_code", "StatusCode"},
			{"latency_ms", "LatencyMs"},
			{"success", "Success"},
			{"failure_reason", "FailureReason"},
		},
	},
}

// ClickHouseScanner materialises one ClickHouse-backed relation.
type ClickHouseScanner struct {
	Rel    Relation
	URL    string // ClickHouse HTTP endpoint
	User   string // HTTP Basic credentials; ClickHouse rejects anonymous reads
	Pass   string
	Client *http.Client
}

func (s *ClickHouseScanner) Relation() Relation { return s.Rel }

// statement returns the SQL this scanner will run. Split out from Scan so the
// isolation properties can be asserted without a live ClickHouse.
func (s *ClickHouseScanner) statement(limit int) (string, error) {
	t, ok := chTables[s.Rel.Name]
	if !ok {
		return "", fmt.Errorf("clickhouse scanner: no physical mapping for relation %q", s.Rel.Name)
	}
	projections := make([]string, len(t.columns))
	for i, c := range t.columns {
		projections[i] = fmt.Sprintf("%s AS %s", c.expr, c.logical)
	}
	// LIMIT is an int the engine chose from the budget, never user text.
	return fmt.Sprintf("SELECT %s FROM %s WHERE %s LIMIT %d FORMAT JSONCompact",
		strings.Join(projections, ", "), t.table, t.tenantPred, limit), nil
}

func (s *ClickHouseScanner) Scan(ctx context.Context, tenantID string, limit int) (*Rows, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("clickhouse scanner %s: refusing to scan with an empty tenant", s.Rel.Name)
	}
	stmt, err := s.statement(limit)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("param_tenant", tenantID) // bound, not interpolated
	endpoint := s.URL
	if !strings.Contains(endpoint, "?") {
		endpoint += "?"
	} else {
		endpoint += "&"
	}
	endpoint += q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(stmt))
	if err != nil {
		return nil, err
	}
	// ClickHouse refuses anonymous reads ("Authentication failed: password is
	// incorrect, or there is no user with such name"). Omitting this made every
	// ClickHouse-backed relation fail against a real server while every unit
	// test passed, because httptest does not check credentials.
	if s.User != "" {
		req.SetBasicAuth(s.User, s.Pass)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clickhouse scanner %s: %w", s.Rel.Name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse scanner %s: status %d: %s",
			s.Rel.Name, resp.StatusCode, strings.TrimSpace(firstBytes(body, 200)))
	}

	var payload struct {
		Meta []struct {
			Name string `json:"name"`
		} `json:"meta"`
		Data [][]any `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("clickhouse scanner %s: decode: %w", s.Rel.Name, err)
	}

	rows := &Rows{Values: payload.Data}
	for _, m := range payload.Meta {
		rows.Columns = append(rows.Columns, m.Name)
	}
	if len(rows.Columns) == 0 {
		rows.Columns = s.Rel.Columns
	}
	return rows, nil
}

func firstBytes(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
