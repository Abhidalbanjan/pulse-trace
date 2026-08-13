package handler

// Synthetic uptime / SLA timeline (Synthetics · E2).
//
// GetResults gives a 1-hour operational snapshot; this gives the reliability
// story over a window: an overall uptime %, and a bucketed red/green
// availability strip an operator (or an SLA report) reads at a glance. The
// bucketing/aggregation is done in ClickHouse; the classification and rollup are
// pure so they're unit-tested without a database.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

// Availability classification thresholds. A bucket aggregates many probe results
// (possibly across steps and the poll interval), so "up" allows a hair of noise
// rather than demanding a literal 100%.
const (
	uptimeUpThresholdPct = 99.5
)

// UptimeBucket is one cell of the availability strip.
type UptimeBucket struct {
	Start     string  `json:"start"`
	Total     int64   `json:"total"`
	Success   int64   `json:"success"`
	UptimePct float64 `json:"uptime_pct"`
	Status    string  `json:"status"` // up | degraded | down | no-data
}

// UptimeSummary is the whole window: an overall SLA number plus the strip.
type UptimeSummary struct {
	Target    string         `json:"target"`
	UptimePct float64        `json:"uptime_pct"`
	Total     int64          `json:"total"`
	Success   int64          `json:"success"`
	Buckets   []UptimeBucket `json:"buckets"`
}

// uptimeRawBucket is one ClickHouse-aggregated bucket before classification.
type uptimeRawBucket struct {
	Start   string
	Total   int64
	Success int64
}

// classifyUptime maps a bucket's success ratio to a status. Kept separate so the
// thresholds are trivially testable.
func classifyUptime(total, success int64) (float64, string) {
	if total <= 0 {
		return 0, "no-data"
	}
	pct := float64(success) / float64(total) * 100
	switch {
	case pct >= uptimeUpThresholdPct:
		return pct, "up"
	case success == 0:
		return pct, "down"
	default:
		return pct, "degraded"
	}
}

// computeUptime rolls raw per-bucket aggregates into the availability strip plus
// an overall uptime %. Pure: no clock, no DB. Buckets with no probes are kept as
// "no-data" so a gap in coverage is visible rather than silently dropped, but
// they don't count against (or toward) the overall SLA number.
func computeUptime(target string, raw []uptimeRawBucket) UptimeSummary {
	out := UptimeSummary{Target: target, Buckets: make([]UptimeBucket, 0, len(raw))}
	var totalChecks, totalSuccess int64
	for _, b := range raw {
		pct, status := classifyUptime(b.Total, b.Success)
		out.Buckets = append(out.Buckets, UptimeBucket{
			Start:     b.Start,
			Total:     b.Total,
			Success:   b.Success,
			UptimePct: pct,
			Status:    status,
		})
		totalChecks += b.Total
		totalSuccess += b.Success
	}
	out.Total = totalChecks
	out.Success = totalSuccess
	if totalChecks > 0 {
		out.UptimePct = float64(totalSuccess) / float64(totalChecks) * 100
	}
	return out
}

// uptimeBucketSeconds picks a bucket width that yields roughly targetBuckets
// cells across the window, floored at 60s so a strip never has sub-minute noise.
// Pure and unit-tested (bucketing math is easy to get subtly wrong).
func uptimeBucketSeconds(windowSeconds, targetBuckets int64) int64 {
	if targetBuckets < 1 {
		targetBuckets = 1
	}
	b := windowSeconds / targetBuckets
	if b < 60 {
		b = 60
	}
	return b
}

// GetUptime returns the uptime % and availability strip for one check over an
// optional [from,to] window (default: last 24h).
//
//	GET /api/v1/synthetics/uptime?target=<check-name-or-url>&from=&to=
func (h *SyntheticsHandler) GetUptime(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "missing target query param", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	to := parseUptimeBound(r.URL.Query().Get("to"), now)
	from := parseUptimeBound(r.URL.Query().Get("from"), to.Add(-24*time.Hour))
	if !from.Before(to) {
		from = to.Add(-24 * time.Hour)
	}

	bucketSec := uptimeBucketSeconds(int64(to.Sub(from).Seconds()), 48)

	tenant := tenantFromRequest(r)
	ch := &clickHouseClient{URL: h.ClickHouseURL}
	// bucketSec is a validated int, safe to inline; all user values are bind params.
	query := fmt.Sprintf(`
		SELECT
			toStartOfInterval(Timestamp, INTERVAL %d SECOND) AS bucket,
			count() AS total,
			sum(Success) AS success
		FROM pulsetrace.synthetic_results
		WHERE TenantID = {tenant:String}
			AND (CheckName = {target:String} OR URL = {target:String})
			AND Timestamp >= toDateTime64({from:String}, 3)
			AND Timestamp <= toDateTime64({to:String}, 3)
		GROUP BY bucket
		ORDER BY bucket ASC
		FORMAT JSON
	`, bucketSec)

	resp, err := ch.queryScoped(tenant, query, map[string]string{
		"target": stringParam(target),
		"from":   stringParam(from.Format("2006-01-02 15:04:05.000")),
		"to":     stringParam(to.Format("2006-01-02 15:04:05.000")),
	})
	if err != nil {
		log.Printf("[SyntheticsHandler] uptime query failed: %v", err)
		http.Error(w, "failed to query synthetics", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_ = json.NewEncoder(w).Encode(computeUptime(target, nil))
		return
	}
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "analytics engine returned error", http.StatusInternalServerError)
		return
	}

	var result struct {
		Data []struct {
			Bucket  string `json:"bucket"`
			Total   string `json:"total"`   // count() → UInt64 as JSON string
			Success string `json:"success"` // sum(UInt8) → UInt64 as JSON string
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[SyntheticsHandler] failed to decode uptime response: %v", err)
		http.Error(w, "failed to decode analytics response", http.StatusInternalServerError)
		return
	}

	raw := make([]uptimeRawBucket, 0, len(result.Data))
	for _, d := range result.Data {
		total, _ := strconv.ParseInt(d.Total, 10, 64)
		success, _ := strconv.ParseInt(d.Success, 10, 64)
		raw = append(raw, uptimeRawBucket{Start: d.Bucket, Total: total, Success: success})
	}

	_ = json.NewEncoder(w).Encode(computeUptime(target, raw))
}

// parseUptimeBound parses an RFC3339 bound, falling back to def for empty or
// malformed input so a bad query param degrades to the default window rather than
// failing the request.
func parseUptimeBound(raw string, def time.Time) time.Time {
	if raw == "" {
		return def
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return def
	}
	return t.UTC()
}
