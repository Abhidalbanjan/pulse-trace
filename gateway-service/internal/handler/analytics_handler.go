package handler

import (
	"bytes"
	"io"
	"log"
	"net/http"
)

type AnalyticsHandler struct {
	ClickHouseURL string
}

func NewAnalyticsHandler(clickhouseURL string) *AnalyticsHandler {
	return &AnalyticsHandler{
		ClickHouseURL: clickhouseURL,
	}
}

// GetTraceAnalytics queries ClickHouse for trace latency percentiles and aggregations
func (h *AnalyticsHandler) GetTraceAnalytics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// 1. Construct ClickHouse SQL query
	// We want P50, P90, P99 over time buckets
	query := `
		SELECT 
			toStartOfMinute(Timestamp) as time_bucket,
			count() as total_traces,
			quantile(0.50)(Duration / 1000000.0) as p50_ms,
			quantile(0.90)(Duration / 1000000.0) as p90_ms,
			quantile(0.99)(Duration / 1000000.0) as p99_ms
		FROM pulsetrace.otel_traces
		WHERE ParentSpanId = '' AND Timestamp >= now() - INTERVAL 1 HOUR
		GROUP BY time_bucket
		ORDER BY time_bucket ASC
		FORMAT JSON
	`

	req, err := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(query))
	if err != nil {
		log.Printf("[AnalyticsHandler] Failed to create request: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	
	// ClickHouse authentication
	req.SetBasicAuth("pulsetrace", "pulsetrace_secret")

	client := &http.Client{}
	resp, err := client.Do(req)
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

	// Stream ClickHouse JSON response directly to the client
	io.Copy(w, resp.Body)
}
