package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
)

type AnalyticsHandler struct {
	ch *clickHouseClient
}

func NewAnalyticsHandler(clickhouseURL string) *AnalyticsHandler {
	return &AnalyticsHandler{ch: &clickHouseClient{URL: clickhouseURL}}
}

// GetTraceAnalytics queries ClickHouse for trace latency percentiles and volume, filterable by
// time range, service name, and HTTP route.
func (h *AnalyticsHandler) GetTraceAnalytics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	_, sqlInterval, bucketExpr := resolveInterval(r.URL.Query().Get("interval"))

	where, params, err := buildTraceAnalyticsWhere(r.URL.Query(), tenantFromRequest(r), sqlInterval)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	query := fmt.Sprintf(`
		SELECT
			%s as time_bucket,
			count() as total_traces,
			quantile(0.50)(Duration / 1000000.0) as p50_ms,
			quantile(0.90)(Duration / 1000000.0) as p90_ms,
			quantile(0.99)(Duration / 1000000.0) as p99_ms
		FROM pulsetrace.otel_traces
		WHERE %s
		GROUP BY time_bucket
		ORDER BY time_bucket ASC
		FORMAT JSON
	`, bucketExpr, where)

	resp, err := h.ch.query(query, params)
	if err != nil {
		log.Printf("[AnalyticsHandler] Failed to execute query: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Table doesn't exist yet (no traces received by OTel Collector)
		io.WriteString(w, `{"data": []}`)
		return
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[AnalyticsHandler] ClickHouse returned %d: %s", resp.StatusCode, string(body))
		http.Error(w, "analytics engine returned error", http.StatusInternalServerError)
		return
	}

	io.Copy(w, resp.Body)
}

// buildTraceAnalyticsWhere assembles the ClickHouse WHERE clause and bind
// params for the trace analytics query from the request's filters, always
// tenant-scoped and restricted to root spans within the resolved interval.
//
// Beyond exact service/route IN-lists it supports regex depth: route_regex and
// operation_regex match SpanAttributes['http.route'] / SpanName via ClickHouse's
// RE2 match(). Each pattern is validated with Go's regexp (same RE2 dialect)
// before it's sent, so a bad pattern returns 400 here rather than a ClickHouse
// error — and is passed as a bind param, never concatenated into the SQL.
func buildTraceAnalyticsWhere(q url.Values, tenant, sqlInterval string) (string, map[string]string, error) {
	where := fmt.Sprintf("%s AND ParentSpanId = '' AND Timestamp >= now() - INTERVAL %s", tenantClause, sqlInterval)
	params := map[string]string{"tenant": tenant}

	if services := q["service"]; len(services) > 0 {
		where += " AND ServiceName IN {services:Array(String)}"
		params["services"] = arrayParam(services)
	}
	if routes := q["route"]; len(routes) > 0 {
		where += " AND SpanAttributes['http.route'] IN {routes:Array(String)}"
		params["routes"] = arrayParam(routes)
	}
	if pattern := q.Get("route_regex"); pattern != "" {
		if _, err := regexp.Compile(pattern); err != nil {
			return "", nil, fmt.Errorf("invalid route_regex %q: %v", pattern, err)
		}
		where += " AND match(SpanAttributes['http.route'], {route_regex:String})"
		params["route_regex"] = pattern
	}
	if pattern := q.Get("operation_regex"); pattern != "" {
		if _, err := regexp.Compile(pattern); err != nil {
			return "", nil, fmt.Errorf("invalid operation_regex %q: %v", pattern, err)
		}
		where += " AND match(SpanName, {operation_regex:String})"
		params["operation_regex"] = pattern
	}

	return where, params, nil
}

// GetTraceFacets returns the distinct service names and HTTP routes seen in stored traces,
// for populating the Trace Analytics facet sidebar with real data.
func (h *AnalyticsHandler) GetTraceFacets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	tenantParams := map[string]string{"tenant": tenantFromRequest(r)}

	serviceResp, err := h.ch.query(`
		SELECT DISTINCT ServiceName as name
		FROM pulsetrace.otel_traces
		WHERE `+tenantClause+` AND ParentSpanId = '' AND Timestamp >= now() - INTERVAL 7 DAY
		ORDER BY name
		LIMIT 50
		FORMAT JSON
	`, tenantParams)
	if err != nil {
		log.Printf("[AnalyticsHandler] Failed to query service facets: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer serviceResp.Body.Close()

	routeResp, err := h.ch.query(`
		SELECT DISTINCT SpanAttributes['http.route'] as name
		FROM pulsetrace.otel_traces
		WHERE `+tenantClause+` AND Timestamp >= now() - INTERVAL 7 DAY AND SpanAttributes['http.route'] != ''
		ORDER BY name
		LIMIT 50
		FORMAT JSON
	`, tenantParams)
	if err != nil {
		log.Printf("[AnalyticsHandler] Failed to query route facets: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer routeResp.Body.Close()

	type row struct {
		Name string `json:"name"`
	}
	type chResult struct {
		Data []row `json:"data"`
	}

	extract := func(resp *http.Response) []string {
		if resp.StatusCode != http.StatusOK {
			return []string{}
		}
		var result chResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return []string{}
		}
		names := make([]string, 0, len(result.Data))
		for _, r := range result.Data {
			names = append(names, r.Name)
		}
		return names
	}

	out := map[string][]string{
		"services": extract(serviceResp),
		"routes":   extract(routeResp),
	}
	_ = json.NewEncoder(w).Encode(out)
}
