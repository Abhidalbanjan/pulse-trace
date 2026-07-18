// Package query executes the tool calls the chat LLM decides it needs
// (search logs, search traces, query a metric) against gateway-service's
// real, already-existing ClickHouse/Quickwit-backed endpoints, and formats
// the result into a compact text block the LLM can read back and turn into
// a natural-language answer. This is what makes the chat's "natural
// language query experience" real: the model is never asked to invent an
// answer from its own knowledge about the user's telemetry — it only ever
// summarizes data that was actually fetched here.
package query

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Executor calls gateway-service on behalf of the chat handler, forwarding
// the end user's own bearer token so query results are scoped by the same
// auth/RBAC rules that would apply if the user hit these endpoints directly
// — the LLM never gets broader data access than the person asking it.
type Executor struct {
	gatewayURL string
	client     *http.Client
}

func NewExecutor(gatewayURL string) *Executor {
	return &Executor{
		gatewayURL: strings.TrimRight(gatewayURL, "/"),
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Tool names the chat LLM is told about (see llm/tools.go's system prompt).
const (
	ToolSearchLogs   = "search_logs"
	ToolSearchTraces = "search_traces"
	ToolQueryMetric  = "query_metric"
)

// Run dispatches to the right gateway-service endpoint and returns a
// compact, human-readable text block summarizing the result — never raw
// JSON — so it can be dropped straight into a follow-up LLM message.
func (e *Executor) Run(ctx context.Context, token string, tool string, args map[string]string) (string, error) {
	switch tool {
	case ToolSearchLogs:
		return e.searchLogs(ctx, token, args)
	case ToolSearchTraces:
		return e.searchTraces(ctx, token, args)
	case ToolQueryMetric:
		return e.queryMetric(ctx, token, args)
	default:
		return "", fmt.Errorf("unknown tool %q", tool)
	}
}

func (e *Executor) get(ctx context.Context, token, path string, query url.Values) (map[string]interface{}, error) {
	reqURL := e.gatewayURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to gateway failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var out map[string]interface{}
	// Both array-rooted (`{"data": [...]}`) and object-rooted responses are
	// normalized to a map here; array-rooted callers unwrap "data" below.
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode gateway response: %w", err)
	}
	return out, nil
}

// searchLogs calls GET /api/v1/logs?service=&level=&q=&limit= (Quickwit-backed
// full text log search, see log-service/internal/handler/log_handler.go).
func (e *Executor) searchLogs(ctx context.Context, token string, args map[string]string) (string, error) {
	q := url.Values{}
	for _, k := range []string{"service", "level", "trace_id", "q"} {
		if v := args[k]; v != "" {
			q.Set(k, v)
		}
	}
	q.Set("limit", "10")

	result, err := e.get(ctx, token, "/api/v1/logs", q)
	if err != nil {
		return "", err
	}

	rows, _ := result["data"].([]interface{})
	if len(rows) == 0 {
		return "No matching logs found.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d matching log(s) (showing up to 10):\n", len(rows))
	for _, r := range rows {
		row, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "- [%v] %v (%v): %v\n", row["level"], row["service_name"], row["timestamp"], truncate(fmt.Sprint(row["message"]), 200))
	}
	return b.String(), nil
}

// searchTraces calls GET /api/v1/analytics/traces?interval=&service=&route=
// (gateway-service/internal/handler/analytics_handler.go, ClickHouse-backed).
func (e *Executor) searchTraces(ctx context.Context, token string, args map[string]string) (string, error) {
	q := url.Values{}
	if v := args["service"]; v != "" {
		q.Set("service", v)
	}
	if v := args["route"]; v != "" {
		q.Set("route", v)
	}
	q.Set("interval", nonEmpty(args["interval"], "1h"))

	result, err := e.get(ctx, token, "/api/v1/analytics/traces", q)
	if err != nil {
		return "", err
	}

	rows, _ := result["data"].([]interface{})
	if len(rows) == 0 {
		return "No matching traces found in the requested window.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d matching trace/span record(s) (showing up to 10):\n", len(rows))
	for i, r := range rows {
		if i >= 10 {
			break
		}
		row, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "- %v\n", row)
	}
	return b.String(), nil
}

// queryMetric calls GET /api/v1/metrics/query?metric=&type=&service=&interval=
// (gateway-service/internal/handler/metrics_handler.go, ClickHouse-backed).
func (e *Executor) queryMetric(ctx context.Context, token string, args map[string]string) (string, error) {
	metric := args["metric"]
	metricType := nonEmpty(args["type"], "gauge")
	if metric == "" {
		return "", fmt.Errorf("query_metric requires a 'metric' name")
	}

	q := url.Values{}
	q.Set("metric", metric)
	q.Set("type", metricType)
	q.Set("interval", nonEmpty(args["interval"], "1h"))
	if v := args["service"]; v != "" {
		q.Set("service", v)
	}

	result, err := e.get(ctx, token, "/api/v1/metrics/query", q)
	if err != nil {
		return "", err
	}

	series, _ := result["series"].(map[string]interface{})
	if len(series) == 0 {
		return fmt.Sprintf("No data points found for metric %q in the requested window.", metric), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Metric %q (%s):\n", metric, metricType)
	for svc, pointsRaw := range series {
		points, _ := pointsRaw.([]interface{})
		if len(points) == 0 {
			continue
		}
		last, _ := points[len(points)-1].(map[string]interface{})
		fmt.Fprintf(&b, "- %s: latest value %v (avg over %d buckets in window), min %v, max %v\n",
			svc, last["value"], len(points), last["min_value"], last["max_value"])
	}
	return b.String(), nil
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
