package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pulsetrace/shared/client"
)

// serviceBaseline tracks a per-service exponential moving average (EWMA) of
// p99 latency, built up from real polls, plus how many samples have gone
// into it. We require a minimum sample count before we trust the baseline
// enough to fire a warning, so a service isn't flagged the moment it's first seen.
type serviceBaseline struct {
	ewmaP99Ms float64
	samples   int
}

// AnomalyDetector polls real per-service RED metrics (rate/error/duration)
// computed directly from ClickHouse trace data and flags services whose
// current p99 latency has drifted meaningfully above their own recent
// baseline. This replaces an earlier placeholder that generated
// rand.Float64() as fake latency for a single hardcoded service name —
// everything here now reads real ingested telemetry.
//
// This queries ClickHouse directly (the same otel_traces table
// gateway-service's ListServices endpoint uses) rather than calling that
// endpoint through the gateway, because the gateway's /api/v1/services route
// sits behind AuthMiddleware and requires a user Bearer token — there's no
// service-to-service auth token minting in this codebase yet, and adding
// this route to the unauthenticated allowlist would expose per-service RED
// metrics to anyone who can reach the gateway. Direct ClickHouse access
// mirrors the pattern already used by gateway-service, topology-service, etc.
type AnomalyDetector struct {
	topoclient         *client.TopologyClient
	clickhouseURL      string
	clickhouseUser     string
	clickhousePassword string
	httpClient         *http.Client
	baselines          map[string]*serviceBaseline
}

func NewAnomalyDetector(topoclient *client.TopologyClient) *AnomalyDetector {
	return &AnomalyDetector{
		topoclient:         topoclient,
		clickhouseURL:      envOrDefault("CLICKHOUSE_URL", "http://clickhouse:8123"),
		clickhouseUser:     envOrDefault("CLICKHOUSE_USER", "pulsetrace"),
		clickhousePassword: envOrDefault("CLICKHOUSE_PASSWORD", "pulsetrace_secret"),
		httpClient:         &http.Client{Timeout: 8 * time.Second},
		baselines:          make(map[string]*serviceBaseline),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// serviceRow mirrors one row of the RED-metrics query below — the same shape
// gateway-service's ListServices returns (see
// gateway-service/internal/handler/service_handler.go).
type serviceRow struct {
	Service  string  `json:"service"`
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	P99Ms    float64 `json:"p99_ms"`
}

type clickhouseJSONResponse struct {
	Data []serviceRow `json:"data"`
}

const redMetricsQuery = `
	SELECT
		ServiceName as service,
		count() as requests,
		countIf(StatusCode = 'STATUS_CODE_ERROR') as errors,
		quantile(0.99)(Duration / 1000000.0) as p99_ms
	FROM pulsetrace.otel_traces
	WHERE ParentSpanId = '' AND Timestamp >= now() - INTERVAL 15 MINUTE
	GROUP BY service
	ORDER BY requests DESC
	FORMAT JSON
`

const (
	pollInterval = 15 * time.Second
	ewmaAlpha    = 0.2 // smoothing factor for the rolling baseline

	// warnMultiplier: fire a PREDICTIVE_WARNING when a service's current p99
	// is at least this many times its own recent baseline.
	warnMultiplier = 1.6

	// minSamplesToWarn: don't fire until a service has enough poll history to
	// trust the baseline — avoids flagging a service the moment it's first seen.
	minSamplesToWarn = 4

	// minRequestsToConsider: skip near-idle services in a given window; a p99
	// computed from a handful of requests is too noisy to act on.
	minRequestsToConsider = 5
)

// Start begins background polling for real, statistically-grounded latency
// anomalies. Each service gets its own EWMA baseline built from its own
// history, so a service that's always slow doesn't perpetually alert, and a
// normally-fast service that suddenly regresses does.
func (a *AnomalyDetector) Start(ctx context.Context) {
	log.Printf("anomaly_detector: started, polling %s every %s", a.clickhouseURL, pollInterval)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pollOnce(ctx)
		}
	}
}

func (a *AnomalyDetector) pollOnce(ctx context.Context) {
	rows, err := a.fetchServiceMetrics(ctx)
	if err != nil {
		log.Printf("anomaly_detector: failed to fetch service metrics: %v", err)
		return
	}

	for _, row := range rows {
		if row.Requests < minRequestsToConsider {
			continue
		}

		baseline, ok := a.baselines[row.Service]
		if !ok {
			// First time we've seen this service: seed the baseline with its
			// current p99 rather than comparing against zero.
			a.baselines[row.Service] = &serviceBaseline{ewmaP99Ms: row.P99Ms, samples: 1}
			continue
		}

		// Compare against the *existing* baseline before folding this sample in,
		// so the update below doesn't chase its own tail.
		if baseline.samples >= minSamplesToWarn && baseline.ewmaP99Ms > 0 && row.P99Ms >= baseline.ewmaP99Ms*warnMultiplier {
			log.Printf("anomaly_detector: ⚠️ %s p99 latency %.1fms is %.1fx its baseline (%.1fms) — PREDICTIVE_WARNING",
				row.Service, row.P99Ms, row.P99Ms/baseline.ewmaP99Ms, baseline.ewmaP99Ms)

			if err := a.topoclient.UpdateServiceState(ctx, row.Service, "PREDICTIVE_WARNING"); err != nil {
				log.Printf("anomaly_detector: failed to update predictive state for %s: %v", row.Service, err)
			}
		}

		baseline.ewmaP99Ms = (ewmaAlpha * row.P99Ms) + ((1 - ewmaAlpha) * baseline.ewmaP99Ms)
		baseline.samples++
	}
}

// fetchServiceMetrics queries ClickHouse directly for real per-service RED
// metrics over the last 15 minutes — recent enough to catch a genuine
// regression quickly without reacting to single-request noise.
func (a *AnomalyDetector) fetchServiceMetrics(ctx context.Context) ([]serviceRow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.clickhouseURL, bytes.NewBufferString(redMetricsQuery))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(a.clickhouseUser, a.clickhousePassword)

	resp, err := a.httpClient.Do(req)
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
