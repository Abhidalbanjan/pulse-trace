package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// ServiceHandler powers the APM Service Page: per-service and per-resource
// RED metrics (Rate, Errors, Duration) computed directly from ClickHouse otel_traces.
type ServiceHandler struct {
	ch *clickHouseClient
}

func NewServiceHandler(clickhouseURL string) *ServiceHandler {
	return &ServiceHandler{ch: &clickHouseClient{URL: clickhouseURL}}
}

func (h *ServiceHandler) writeErrOrEmpty(w http.ResponseWriter, resp *http.Response, err error, logPrefix string) bool {
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

// ListServices returns every service seen in the last N minutes with an at-a-glance
// RED summary - the Datadog "APM > Services" landing list.
func (h *ServiceHandler) ListServices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sqlInterval := "15 MINUTE"
	if raw := r.URL.Query().Get("interval"); raw != "" {
		if _, ok := intervalToSQL[raw]; ok {
			sqlInterval = intervalToSQL[raw]
		}
	}

	query := fmt.Sprintf(`
		SELECT
			ServiceName as service,
			count() as requests,
			countIf(StatusCode = 'STATUS_CODE_ERROR') as errors,
			quantile(0.50)(Duration / 1000000.0) as p50_ms,
			quantile(0.90)(Duration / 1000000.0) as p90_ms,
			quantile(0.99)(Duration / 1000000.0) as p99_ms
		FROM pulsetrace.otel_traces
		WHERE ResourceAttributes['tenant.id'] = {tenant:String} AND ParentSpanId = '' AND Timestamp >= now() - INTERVAL %s
		GROUP BY service
		ORDER BY requests DESC
		FORMAT JSON
	`, sqlInterval)

	resp, err := h.ch.queryScoped(tenantFromRequest(r), query, nil)
	if !h.writeErrOrEmpty(w, resp, err, "ServiceHandler.ListServices") {
		return
	}
	defer resp.Body.Close()

	// Annotate each service with a health score (Services · E2) derived from its
	// RED signals, so the list can rank and colour services by health at a glance.
	// On any decode hiccup we fall back to streaming the raw payload unchanged.
	var parsed struct {
		Data []map[string]interface{} `json:"data"`
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil || json.Unmarshal(body, &parsed) != nil {
		w.Write(body)
		return
	}
	for _, row := range parsed.Data {
		requests := toFloat64(row["requests"])
		errors := toFloat64(row["errors"])
		errorRatePct := 0.0
		if requests > 0 {
			errorRatePct = errors / requests * 100
		}
		score, band := healthScore(errorRatePct, toFloat64(row["p99_ms"]), defaultLatencyBudgetMs)
		row["health_score"] = score
		row["health_band"] = band
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": parsed.Data})
}

// defaultLatencyBudgetMs is the p99 a service is expected to stay under before
// latency starts eroding its health score. A pragmatic default in the absence of
// a per-service SLO; the scorer degrades smoothly past it rather than cliff-edging.
const defaultLatencyBudgetMs = 500.0

// healthScore condenses a service's RED posture into a 0–100 score and a band.
// It starts from a perfect 100 and deducts for the two things that actually hurt
// users: error rate (each 1% of failed requests costs ~10 points) and tail
// latency over budget (each full multiple of the budget over 1× costs 40). Pure
// and unit-tested so the scoring contract is stable and explainable.
func healthScore(errorRatePct, p99Ms, latencyBudgetMs float64) (int, string) {
	score := 100.0
	if errorRatePct > 0 {
		score -= errorRatePct * 10
	}
	if latencyBudgetMs > 0 && p99Ms > latencyBudgetMs {
		score -= (p99Ms/latencyBudgetMs - 1) * 40
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	s := int(score + 0.5)
	switch {
	case s >= 90:
		return s, "healthy"
	case s >= 70:
		return s, "degraded"
	case s >= 40:
		return s, "unhealthy"
	default:
		return s, "critical"
	}
}

// GetServiceDetail returns the RED time series and per-resource (operation) breakdown
// for a single service - the Datadog "Service Page".
func (h *ServiceHandler) GetServiceDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	service := r.PathValue("name")
	if service == "" {
		http.Error(w, "missing service name", http.StatusBadRequest)
		return
	}

	_, sqlInterval, bucketExpr := resolveInterval(r.URL.Query().Get("interval"))
	params := map[string]string{"service": stringParam(service), "tenant": tenantFromRequest(r)}

	summaryQuery := fmt.Sprintf(`
		SELECT
			count() as requests,
			countIf(StatusCode = 'STATUS_CODE_ERROR') as errors,
			quantile(0.50)(Duration / 1000000.0) as p50_ms,
			quantile(0.90)(Duration / 1000000.0) as p90_ms,
			quantile(0.99)(Duration / 1000000.0) as p99_ms
		FROM pulsetrace.otel_traces
		WHERE ResourceAttributes['tenant.id'] = {tenant:String} AND ParentSpanId = '' AND ServiceName = {service:String} AND Timestamp >= now() - INTERVAL %s
		FORMAT JSON
	`, sqlInterval)

	timeseriesQuery := fmt.Sprintf(`
		SELECT
			%s as time_bucket,
			count() as requests,
			countIf(StatusCode = 'STATUS_CODE_ERROR') as errors,
			quantile(0.50)(Duration / 1000000.0) as p50_ms,
			quantile(0.90)(Duration / 1000000.0) as p90_ms,
			quantile(0.99)(Duration / 1000000.0) as p99_ms
		FROM pulsetrace.otel_traces
		WHERE ResourceAttributes['tenant.id'] = {tenant:String} AND ParentSpanId = '' AND ServiceName = {service:String} AND Timestamp >= now() - INTERVAL %s
		GROUP BY time_bucket
		ORDER BY time_bucket ASC
		FORMAT JSON
	`, bucketExpr, sqlInterval)

	resourcesQuery := fmt.Sprintf(`
		SELECT
			SpanName as operation,
			count() as requests,
			countIf(StatusCode = 'STATUS_CODE_ERROR') as errors,
			quantile(0.50)(Duration / 1000000.0) as p50_ms,
			quantile(0.90)(Duration / 1000000.0) as p90_ms,
			quantile(0.99)(Duration / 1000000.0) as p99_ms,
			sum(Duration) / 1000000.0 as total_ms
		FROM pulsetrace.otel_traces
		WHERE ResourceAttributes['tenant.id'] = {tenant:String} AND ParentSpanId = '' AND ServiceName = {service:String} AND Timestamp >= now() - INTERVAL %s
		GROUP BY operation
		ORDER BY requests DESC
		LIMIT 50
		FORMAT JSON
	`, sqlInterval)

	// Deployment Tracking: RED metrics sliced by service.version, so you can see
	// whether the currently-deployed version regressed vs the one before it.
	versionsQuery := fmt.Sprintf(`
		SELECT
			ResourceAttributes['service.version'] as version,
			count() as requests,
			countIf(StatusCode = 'STATUS_CODE_ERROR') as errors,
			quantile(0.50)(Duration / 1000000.0) as p50_ms,
			quantile(0.90)(Duration / 1000000.0) as p90_ms,
			quantile(0.99)(Duration / 1000000.0) as p99_ms,
			min(Timestamp) as first_seen,
			max(Timestamp) as last_seen
		FROM pulsetrace.otel_traces
		WHERE ResourceAttributes['tenant.id'] = {tenant:String} AND ParentSpanId = '' AND ServiceName = {service:String} AND Timestamp >= now() - INTERVAL %s
		GROUP BY version
		ORDER BY last_seen DESC
		FORMAT JSON
	`, sqlInterval)

	summaryResp, err := h.ch.queryScoped(tenantFromRequest(r), summaryQuery, params)
	if err != nil {
		log.Printf("[ServiceHandler.GetServiceDetail] summary query failed: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer summaryResp.Body.Close()

	timeseriesResp, err := h.ch.queryScoped(tenantFromRequest(r), timeseriesQuery, params)
	if err != nil {
		log.Printf("[ServiceHandler.GetServiceDetail] timeseries query failed: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer timeseriesResp.Body.Close()

	resourcesResp, err := h.ch.queryScoped(tenantFromRequest(r), resourcesQuery, params)
	if err != nil {
		log.Printf("[ServiceHandler.GetServiceDetail] resources query failed: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer resourcesResp.Body.Close()

	versionsResp, err := h.ch.queryScoped(tenantFromRequest(r), versionsQuery, params)
	if err != nil {
		log.Printf("[ServiceHandler.GetServiceDetail] versions query failed: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer versionsResp.Body.Close()

	type chResult struct {
		Data []map[string]interface{} `json:"data"`
	}
	decode := func(resp *http.Response) []map[string]interface{} {
		if resp.StatusCode != http.StatusOK {
			return []map[string]interface{}{}
		}
		var result chResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return []map[string]interface{}{}
		}
		return result.Data
	}

	summaryRows := decode(summaryResp)
	var summary map[string]interface{}
	if len(summaryRows) > 0 {
		summary = summaryRows[0]
	} else {
		summary = map[string]interface{}{"requests": 0, "errors": 0, "p50_ms": 0, "p90_ms": 0, "p99_ms": 0}
	}

	out := map[string]interface{}{
		"service":    service,
		"summary":    summary,
		"timeseries": decode(timeseriesResp),
		"resources":  decode(resourcesResp),
		"versions":   flagRegressions(decode(versionsResp)),
	}
	_ = json.NewEncoder(w).Encode(out)
}

// Regression detection thresholds. A version needs at least minRequestsForComparison
// requests (and so does the version before it) before we trust a rate comparison -
// otherwise 1 error out of 2 requests would look like a 50% error rate "regression".
const (
	minRequestsForComparison    = 10
	errorRateRegressionPoints   = 0.05 // +5 percentage points absolute error rate increase
	latencyRegressionMultiplier = 1.5  // p99 growing to >= 1.5x the previous version's p99
	clickhouseDateTimeLayout    = "2006-01-02 15:04:05"
)

func toFloat64(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}

func toStr(v interface{}) string {
	s, _ := v.(string)
	return s
}

// toFloat coerces a decoded JSON value to float64, tolerating the two shapes
// ClickHouse's FORMAT JSON produces for numerics: a JSON number (Float64 →
// float64) and a quoted string (UInt64 and friends). Returns 0 on anything else.
func toFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}

// flagRegressions compares each version's error rate and p99 latency against the
// version immediately before it (by first_seen) and marks a regression when either
// jumps sharply - without this, spotting "v1.2.3 has 3x the error rate of v1.2.2"
// requires a human to manually eyeball two rows in the versions table.
func flagRegressions(versions []map[string]interface{}) []map[string]interface{} {
	if len(versions) == 0 {
		return versions
	}

	// Work on a copy sorted oldest-first so "previous version" is well-defined,
	// but return results in the original (newest-first) order.
	chronological := make([]map[string]interface{}, len(versions))
	copy(chronological, versions)
	sort.SliceStable(chronological, func(i, j int) bool {
		ti, _ := time.Parse(clickhouseDateTimeLayout, toStr(chronological[i]["first_seen"]))
		tj, _ := time.Parse(clickhouseDateTimeLayout, toStr(chronological[j]["first_seen"]))
		return ti.Before(tj)
	})

	type stats struct {
		version   string
		requests  float64
		errorRate float64
		p99       float64
	}
	prev := (*stats)(nil)
	for _, v := range chronological {
		requests := toFloat64(v["requests"])
		errors := toFloat64(v["errors"])
		p99 := toFloat64(v["p99_ms"])
		errorRate := 0.0
		if requests > 0 {
			errorRate = errors / requests
		}
		current := stats{version: toStr(v["version"]), requests: requests, errorRate: errorRate, p99: p99}

		if prev != nil && prev.requests >= minRequestsForComparison && current.requests >= minRequestsForComparison {
			errorDelta := current.errorRate - prev.errorRate
			errorRegression := errorDelta >= errorRateRegressionPoints
			latencyRegression := prev.p99 > 0 && current.p99 >= prev.p99*latencyRegressionMultiplier

			v["is_regression"] = errorRegression || latencyRegression
			v["previous_version"] = prev.version
			v["error_rate_delta_pct"] = errorDelta * 100
			if prev.p99 > 0 {
				v["p99_delta_pct"] = ((current.p99 - prev.p99) / prev.p99) * 100
			} else {
				v["p99_delta_pct"] = 0.0
			}
		} else {
			v["is_regression"] = false
		}

		prev = &current
	}

	return versions
}
