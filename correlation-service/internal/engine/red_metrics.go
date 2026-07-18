package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// redMetricsClient queries ClickHouse directly for real per-service RED
// (rate/error/duration) metrics, computed from the same otel_traces table
// gateway-service's ListServices endpoint uses. Shared by AnomalyDetector and
// AlertRuleEvaluator so both poll identical data with one query shape instead
// of drifting out of sync with their own copies.
//
// This queries ClickHouse directly rather than calling gateway-service's
// /api/v1/services, because that route sits behind AuthMiddleware and there's
// no service-to-service auth token minting in this codebase yet — see the
// longer explanation on AnomalyDetector.
type redMetricsClient struct {
	clickhouseURL      string
	clickhouseUser     string
	clickhousePassword string
	httpClient         *http.Client
}

func newRedMetricsClient() *redMetricsClient {
	return &redMetricsClient{
		clickhouseURL:      envOrDefault("CLICKHOUSE_URL", "http://clickhouse:8123"),
		clickhouseUser:     envOrDefault("CLICKHOUSE_USER", "pulsetrace"),
		clickhousePassword: envOrDefault("CLICKHOUSE_PASSWORD", "pulsetrace_secret"),
		httpClient:         &http.Client{Timeout: 8 * time.Second},
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// serviceRow is one row of the RED-metrics query — the same shape
// gateway-service's ListServices returns (see
// gateway-service/internal/handler/service_handler.go).
type serviceRow struct {
	Service  string  `json:"service"`
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	P50Ms    float64 `json:"p50_ms"`
	P90Ms    float64 `json:"p90_ms"`
	P99Ms    float64 `json:"p99_ms"`
}

// ErrorRate returns the percentage (0-100) of requests that errored.
func (s serviceRow) ErrorRate() float64 {
	if s.Requests == 0 {
		return 0
	}
	return (float64(s.Errors) / float64(s.Requests)) * 100.0
}

type clickhouseJSONResponse struct {
	Data []serviceRow `json:"data"`
}

const redMetricsQuery = `
	SELECT
		ServiceName as service,
		count() as requests,
		countIf(StatusCode = 'STATUS_CODE_ERROR') as errors,
		quantile(0.50)(Duration / 1000000.0) as p50_ms,
		quantile(0.90)(Duration / 1000000.0) as p90_ms,
		quantile(0.99)(Duration / 1000000.0) as p99_ms
	FROM pulsetrace.otel_traces
	WHERE ParentSpanId = '' AND Timestamp >= now() - INTERVAL 15 MINUTE
	GROUP BY service
	ORDER BY requests DESC
	FORMAT JSON
`

// fetch queries ClickHouse for real per-service RED metrics over the last 15
// minutes.
func (c *redMetricsClient) fetch(ctx context.Context) ([]serviceRow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.clickhouseURL, bytes.NewBufferString(redMetricsQuery))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.clickhouseUser, c.clickhousePassword)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // no data ingested yet — not an error
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse returned status %d", resp.StatusCode)
	}

	var out clickhouseJSONResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out.Data, nil
}
