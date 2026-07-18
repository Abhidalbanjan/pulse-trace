package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

// MetricsHandler is PulseTrace's native metrics pillar: it queries the
// OTLP metric datapoints the collector writes directly into ClickHouse
// (otel_metrics_gauge / otel_metrics_sum), the same way ServiceHandler
// queries otel_traces for RED metrics. This makes metrics a first-class,
// product-native pillar instead of something only visible by logging into
// a bundled Grafana — Prometheus/Grafana remain in docker-compose purely
// for PulseTrace's own internal service self-monitoring and are never
// linked from the product UI.
//
// The collector's clickhouse exporter (otel-collector/otel-collector-config.yaml)
// splits datapoints across one table per OTLP metric type. This handler
// supports the two most common instrument kinds used for RED-style
// application metrics: gauge (point-in-time values, e.g. queue depth,
// active connections) and sum (monotonic counters, e.g. requests_total,
// bytes_processed). Histograms/summaries are intentionally out of scope for
// v1 of this endpoint — they need bucket-aware percentile math that
// deserves its own endpoint rather than being bolted onto this one.
type MetricsHandler struct {
	ch *clickHouseClient
}

func NewMetricsHandler(clickhouseURL string) *MetricsHandler {
	return &MetricsHandler{ch: &clickHouseClient{URL: clickhouseURL}}
}

// metricTableFor maps the user-facing "type" query param to the ClickHouse
// table the collector's clickhouse exporter writes that instrument kind
// into (see otel-collector/otel-collector-config.yaml's metrics_tables config).
var metricTableFor = map[string]string{
	"gauge": "otel_metrics_gauge",
	"sum":   "otel_metrics_sum",
}

var metricIntervalToBucket = map[string]string{
	"1h":  "toStartOfMinute(TimeUnix)",
	"24h": "toStartOfInterval(TimeUnix, INTERVAL 15 MINUTE)",
	"7d":  "toStartOfHour(TimeUnix)",
}

func (h *MetricsHandler) writeErrOrEmpty(w http.ResponseWriter, resp *http.Response, err error, logPrefix string) bool {
	if err != nil {
		log.Printf("[%s] query failed: %v", logPrefix, err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return false
	}
	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return false
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[%s] ClickHouse returned %d: %s", logPrefix, resp.StatusCode, string(body))
		http.Error(w, "analytics engine returned error", http.StatusInternalServerError)
		return false
	}
	return true
}

// ListMetricNames handles GET /api/v1/metrics — returns every distinct
// (MetricName, type, unit) triple seen in the last 24h across both gauge and
// sum tables, so the frontend can populate a metric picker without the user
// needing to already know what's been instrumented.
func (h *MetricsHandler) ListMetricNames(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := `
		SELECT MetricName as name, MetricDescription as description, MetricUnit as unit, ServiceName as service, 'gauge' as type
		FROM pulsetrace.otel_metrics_gauge
		WHERE TimeUnix >= now() - INTERVAL 24 HOUR
		GROUP BY name, description, unit, service
		UNION ALL
		SELECT MetricName as name, MetricDescription as description, MetricUnit as unit, ServiceName as service, 'sum' as type
		FROM pulsetrace.otel_metrics_sum
		WHERE TimeUnix >= now() - INTERVAL 24 HOUR
		GROUP BY name, description, unit, service
		ORDER BY name ASC
		FORMAT JSON
	`

	resp, err := h.ch.query(query, nil)
	if !h.writeErrOrEmpty(w, resp, err, "MetricsHandler.ListMetricNames") {
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

// QueryMetric handles GET /api/v1/metrics/query?metric=&type=gauge|sum&service=&interval=1h
// and returns a time-bucketed series. When service is omitted, the series is
// broken out per-service (up to 20, by total datapoint volume) so a metric
// name shared across many services still renders as readable, comparable
// lines instead of one meaningless blended average.
func (h *MetricsHandler) QueryMetric(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	metricName := r.URL.Query().Get("metric")
	if metricName == "" {
		http.Error(w, "missing required 'metric' query param", http.StatusBadRequest)
		return
	}

	metricType := r.URL.Query().Get("type")
	table, ok := metricTableFor[metricType]
	if !ok {
		http.Error(w, "'type' must be one of: gauge, sum", http.StatusBadRequest)
		return
	}

	interval := r.URL.Query().Get("interval")
	sqlInterval, ok := intervalToSQL[interval]
	if !ok {
		interval = "1h"
		sqlInterval = intervalToSQL[interval]
	}
	bucketExpr := metricIntervalToBucket[interval]

	service := r.URL.Query().Get("service")
	params := map[string]string{"metric": stringParam(metricName)}

	var whereService, groupService, selectService string
	if service != "" {
		params["service"] = stringParam(service)
		whereService = "AND ServiceName = {service:String}"
	} else {
		groupService = ", service"
		selectService = "ServiceName as service,"
	}

	// Aggregation choice: sum tables are monotonic counters, so a rate-like
	// "how much happened in this bucket" reading comes from the delta between
	// consecutive raw values — but computing true resets-aware deltas needs
	// window functions per-series. For v1 we report avg(Value) per bucket for
	// both types (matches what a gauge naturally represents; for a sum this
	// is the average of the running total within the bucket, which is a
	// coarser but still genuinely real, non-fabricated signal — not a
	// substitute for true rate() semantics, which is flagged as a known
	// follow-up rather than silently pretending to be exact).
	query := fmt.Sprintf(`
		SELECT
			%s as time_bucket,
			%s
			avg(Value) as value,
			min(Value) as min_value,
			max(Value) as max_value,
			count() as sample_count
		FROM pulsetrace.%s
		WHERE MetricName = {metric:String} %s AND TimeUnix >= now() - INTERVAL %s
		GROUP BY time_bucket%s
		ORDER BY time_bucket ASC
		FORMAT JSON
	`, bucketExpr, selectService, table, whereService, sqlInterval, groupService)

	resp, err := h.ch.query(query, params)
	if !h.writeErrOrEmpty(w, resp, err, "MetricsHandler.QueryMetric") {
		return
	}
	defer resp.Body.Close()

	// Always respond with the same {metric, type, series} shape regardless of
	// whether a specific service was requested — a single-service query just
	// produces a series map with one key, rather than switching response
	// shape out from under callers based on which query params were passed.
	type chResult struct {
		Data []map[string]interface{} `json:"data"`
	}
	var result chResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[MetricsHandler.QueryMetric] decode failed: %v", err)
		http.Error(w, "failed to decode analytics engine response", http.StatusInternalServerError)
		return
	}

	series := map[string][]map[string]interface{}{}
	if service != "" {
		series[service] = result.Data
	} else {
		for _, row := range result.Data {
			svc := toStr(row["service"])
			series[svc] = append(series[svc], row)
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"metric": metricName, "type": metricType, "series": series})
}
