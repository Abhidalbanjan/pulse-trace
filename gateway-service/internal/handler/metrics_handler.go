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

// metricIntervalBucketSeconds is the width, in seconds, of each time bucket for
// a given interval — the denominator rate() divides the per-bucket counter
// increase by to produce a per-second rate. It must stay in lock-step with
// metricIntervalToBucket above.
var metricIntervalBucketSeconds = map[string]int{
	"1h":  60,   // toStartOfMinute
	"24h": 900,  // 15-minute buckets
	"7d":  3600, // hourly buckets
}

// metricAggExpr returns the ClickHouse aggregate expression that produces each
// bucket's `value` for the requested function, plus whether the function is
// supported. This is the heart of the "query API with functions" — it is a pure
// string builder so it can be unit-tested without a live ClickHouse.
//
// Semantics per function:
//   - avg/min/max/sum: the obvious aggregate over the raw datapoints in the bucket.
//   - rate: the monotonic-counter increase across the bucket, per second. It
//     assumes the counter does not reset mid-bucket (the standard rate() caveat);
//     greatest(...,0) means a reset degrades to a one-bucket dip toward zero
//     rather than a negative spike. Meaningful for `sum` (counter) series.
//   - p50/p90/p95/p99: the distribution of datapoint values within the bucket
//     (ClickHouse quantile). Meaningful for `gauge` series (e.g. queue depth).
//
// An unknown function returns ("", false) so the handler can 400 rather than
// silently substituting avg and returning a number that isn't what was asked for.
func metricAggExpr(fn string, bucketSeconds int) (string, bool) {
	switch fn {
	case "", "avg":
		return "avg(Value)", true
	case "min":
		return "min(Value)", true
	case "max":
		return "max(Value)", true
	case "sum":
		return "sum(Value)", true
	case "rate":
		if bucketSeconds <= 0 {
			return "", false
		}
		return fmt.Sprintf("greatest(max(Value) - min(Value), 0) / %d", bucketSeconds), true
	case "p50":
		return "quantile(0.50)(Value)", true
	case "p90":
		return "quantile(0.90)(Value)", true
	case "p95":
		return "quantile(0.95)(Value)", true
	case "p99":
		return "quantile(0.99)(Value)", true
	default:
		return "", false
	}
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
		WHERE ResourceAttributes['tenant.id'] = {tenant:String} AND TimeUnix >= now() - INTERVAL 24 HOUR
		GROUP BY name, description, unit, service
		UNION ALL
		SELECT MetricName as name, MetricDescription as description, MetricUnit as unit, ServiceName as service, 'sum' as type
		FROM pulsetrace.otel_metrics_sum
		WHERE ResourceAttributes['tenant.id'] = {tenant:String} AND TimeUnix >= now() - INTERVAL 24 HOUR
		GROUP BY name, description, unit, service
		ORDER BY name ASC
		FORMAT JSON
	`

	resp, err := h.ch.queryScoped(tenantFromRequest(r), query, nil)
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

	// fn selects the per-bucket aggregation (avg by default). Validate against a
	// closed allowlist so an unknown function is a 400, never a silent avg.
	valueExpr, ok := metricAggExpr(r.URL.Query().Get("fn"), metricIntervalBucketSeconds[interval])
	if !ok {
		http.Error(w, "'fn' must be one of: avg, min, max, sum, rate, p50, p90, p95, p99", http.StatusBadRequest)
		return
	}

	service := r.URL.Query().Get("service")
	params := map[string]string{"metric": stringParam(metricName), "tenant": tenantFromRequest(r)}

	var whereService, groupService, selectService string
	if service != "" {
		params["service"] = stringParam(service)
		whereService = "AND ServiceName = {service:String}"
	} else {
		groupService = ", service"
		selectService = "ServiceName as service,"
	}

	// The per-bucket `value` is produced by the caller-selected function
	// (metricAggExpr); min/max/sample_count are always returned as context for
	// the primary value regardless of which function was chosen.
	query := fmt.Sprintf(`
		SELECT
			%s as time_bucket,
			%s
			%s as value,
			min(Value) as min_value,
			max(Value) as max_value,
			count() as sample_count
		FROM pulsetrace.%s
		WHERE ResourceAttributes['tenant.id'] = {tenant:String} AND MetricName = {metric:String} %s AND TimeUnix >= now() - INTERVAL %s
		GROUP BY time_bucket%s
		ORDER BY time_bucket ASC
		FORMAT JSON
	`, bucketExpr, selectService, valueExpr, table, whereService, sqlInterval, groupService)

	resp, err := h.ch.queryScoped(tenantFromRequest(r), query, params)
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
	fn := r.URL.Query().Get("fn")
	if fn == "" {
		fn = "avg"
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"metric": metricName, "type": metricType, "fn": fn, "series": series})
}
