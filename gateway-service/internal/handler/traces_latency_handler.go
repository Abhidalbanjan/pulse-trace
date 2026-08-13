package handler

// Trace latency distribution & percentiles (Traces · E2).
//
// A p99 number tells you the tail is bad; a distribution tells you *why* — is it
// a fat second mode (a slow dependency on some requests) or a long thin tail?
// This computes both: p50/p95/p99 plus a fixed-width histogram of end-to-end
// (root-span) trace durations, so the UI can render a distribution the operator
// can click to drill into the slow traces. Bucketing math is pure & unit-tested.

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
)

const (
	defaultLatencyBuckets = 30
	maxLatencyBuckets     = 100
)

// LatencyBucket is one contiguous [lower,upper) ms bar of the histogram.
type LatencyBucket struct {
	LowerMs float64 `json:"lower_ms"`
	UpperMs float64 `json:"upper_ms"`
	Count   int64   `json:"count"`
}

// niceCeil rounds x up to the nearest "nice" number (1, 2, 5 × 10^n) so histogram
// bucket edges land on human-readable values instead of e.g. 37.4ms.
func niceCeil(x float64) float64 {
	if x <= 0 {
		return 1
	}
	exp := math.Floor(math.Log10(x))
	base := math.Pow(10, exp)
	switch f := x / base; {
	case f <= 1:
		return base
	case f <= 2:
		return 2 * base
	case f <= 5:
		return 5 * base
	default:
		return 10 * base
	}
}

// bucketConfig picks a nice bucket width so [0,maxMs] splits into roughly
// targetBuckets bars, and returns the resulting bucket count. Pure.
func bucketConfig(maxMs float64, targetBuckets int) (widthMs float64, count int) {
	if targetBuckets < 1 {
		targetBuckets = 1
	}
	if maxMs <= 0 {
		return 1, 1
	}
	widthMs = niceCeil(maxMs / float64(targetBuckets))
	count = int(math.Ceil(maxMs / widthMs))
	if count < 1 {
		count = 1
	}
	return widthMs, count
}

// assembleLatencyBuckets expands a sparse bucketIndex→count map into the full
// contiguous set of buckets (zero-filling gaps) so the chart is continuous. Pure.
func assembleLatencyBuckets(sparse map[int]int64, width float64, count int) []LatencyBucket {
	out := make([]LatencyBucket, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, LatencyBucket{
			LowerMs: float64(i) * width,
			UpperMs: float64(i+1) * width,
			Count:   sparse[i],
		})
	}
	return out
}

// GetLatency returns the trace-duration distribution + percentiles for a
// service/operation over a window.
//
//	GET /api/v1/traces/latency?service=&operation=&interval=&buckets=
func (h *TracesHandler) GetLatency(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()

	targetBuckets := defaultLatencyBuckets
	if b, err := strconv.Atoi(q.Get("buckets")); err == nil && b > 0 {
		targetBuckets = b
	}
	if targetBuckets > maxLatencyBuckets {
		targetBuckets = maxLatencyBuckets
	}

	_, sqlInterval, _ := resolveInterval(q.Get("interval"))
	tenant := tenantFromRequest(r)

	// Shared WHERE over root spans (end-to-end trace duration), all values bound.
	where := tenantClause + " AND ParentSpanId = '' AND Timestamp >= now() - INTERVAL " + sqlInterval
	params := map[string]string{"tenant": stringParam(tenant)}
	if svc := q.Get("service"); svc != "" {
		where += " AND ServiceName = {service:String}"
		params["service"] = stringParam(svc)
	}
	if op := q.Get("operation"); op != "" {
		where += " AND SpanName = {operation:String}"
		params["operation"] = stringParam(op)
	}

	// Stats: count, max, and the percentiles (ms).
	statsSQL := fmt.Sprintf(`
		SELECT count() AS c, max(Duration/1000000.0) AS max_ms,
			quantile(0.50)(Duration/1000000.0) AS p50,
			quantile(0.95)(Duration/1000000.0) AS p95,
			quantile(0.99)(Duration/1000000.0) AS p99
		FROM pulsetrace.otel_traces WHERE %s FORMAT JSON`, where)

	statsResp, err := h.ch.queryScoped(tenant, statsSQL, params)
	if err != nil {
		log.Printf("[TracesHandler] latency stats query failed: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer statsResp.Body.Close()

	var stats struct {
		Data []struct {
			C     string  `json:"c"`
			MaxMs float64 `json:"max_ms"`
			P50   float64 `json:"p50"`
			P95   float64 `json:"p95"`
			P99   float64 `json:"p99"`
		} `json:"data"`
	}
	if statsResp.StatusCode != http.StatusOK || json.NewDecoder(statsResp.Body).Decode(&stats) != nil || len(stats.Data) == 0 {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 0, "buckets": []LatencyBucket{}})
		return
	}
	s := stats.Data[0]
	total, _ := strconv.ParseInt(s.C, 10, 64)
	if total == 0 {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 0, "buckets": []LatencyBucket{}})
		return
	}

	width, bucketCount := bucketConfig(s.MaxMs, targetBuckets)

	// Histogram: width is a computed float (not user input), safe to inline.
	histSQL := fmt.Sprintf(`
		SELECT toInt32(floor((Duration/1000000.0)/%g)) AS b, count() AS c
		FROM pulsetrace.otel_traces WHERE %s
		GROUP BY b ORDER BY b ASC FORMAT JSON`, width, where)

	histResp, err := h.ch.queryScoped(tenant, histSQL, params)
	if err != nil {
		log.Printf("[TracesHandler] latency histogram query failed: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer histResp.Body.Close()

	var hist struct {
		Data []struct {
			B int    `json:"b"`
			C string `json:"c"`
		} `json:"data"`
	}
	sparse := map[int]int64{}
	if histResp.StatusCode == http.StatusOK && json.NewDecoder(histResp.Body).Decode(&hist) == nil {
		for _, d := range hist.Data {
			idx := d.B
			if idx < 0 {
				idx = 0
			}
			if idx >= bucketCount { // the max value can land one past the last bucket
				idx = bucketCount - 1
			}
			cnt, _ := strconv.ParseInt(d.C, 10, 64)
			sparse[idx] += cnt
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"service":         q.Get("service"),
		"operation":       q.Get("operation"),
		"count":           total,
		"p50_ms":          s.P50,
		"p95_ms":          s.P95,
		"p99_ms":          s.P99,
		"max_ms":          s.MaxMs,
		"bucket_width_ms": width,
		"buckets":         assembleLatencyBuckets(sparse, width, bucketCount),
	})
}
