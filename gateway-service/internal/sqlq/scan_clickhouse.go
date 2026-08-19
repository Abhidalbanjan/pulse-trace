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
	return s.exec(ctx, tenantID, stmt)
}

// exec runs one statement with the tenant bound as a parameter and decodes
// JSONCompact. Shared by row scans and aggregate push-down so both reach
// ClickHouse the same way — a second transport would be a second place to
// forget the credentials or the bind parameter.
func (s *ClickHouseScanner) exec(ctx context.Context, tenantID, stmt string) (*Rows, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("clickhouse scanner %s: refusing to query with an empty tenant", s.Rel.Name)
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

// ── Aggregator ───────────────────────────────────────────────────────────────
//
// ClickHouse is SQL, so both shapes are a direct translation. The statement is
// still built from the same compile-time mapping and the same bind parameter as
// a row scan: the column name is validated against the catalog and then quoted
// from the mapping's own expression, never from user text.

func (s *ClickHouseScanner) CountAll(ctx context.Context, tenantID string) (int64, error) {
	t, ok := chTables[s.Rel.Name]
	if !ok {
		return 0, fmt.Errorf("clickhouse scanner: no physical mapping for relation %q", s.Rel.Name)
	}
	stmt := fmt.Sprintf("SELECT count() AS c FROM %s WHERE %s FORMAT JSONCompact", t.table, t.tenantPred)
	rows, err := s.exec(ctx, tenantID, stmt)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return 0, nil
	}
	return toInt64(rows.Values[0][0]), nil
}

func (s *ClickHouseScanner) GroupCount(ctx context.Context, tenantID, column string, limit int) (*Rows, error) {
	t, ok := chTables[s.Rel.Name]
	if !ok {
		return nil, fmt.Errorf("clickhouse scanner: no physical mapping for relation %q", s.Rel.Name)
	}
	expr := ""
	for _, c := range t.columns {
		if c.logical == column {
			expr = c.expr
			break
		}
	}
	if expr == "" {
		return nil, fmt.Errorf("clickhouse scanner: %q is not a mapped column of %s", column, s.Rel.Name)
	}
	stmt := fmt.Sprintf(
		"SELECT %s AS %s, count() AS count FROM %s WHERE %s GROUP BY %s ORDER BY count DESC LIMIT %d FORMAT JSONCompact",
		expr, column, t.table, t.tenantPred, expr, limit)
	return s.exec(ctx, tenantID, stmt)
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case string:
		var n int64
		_, _ = fmt.Sscanf(x, "%d", &n)
		return n
	default:
		return 0
	}
}
