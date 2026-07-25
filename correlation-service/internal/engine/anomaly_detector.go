package engine

import (
	"context"
	"log"
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

// ratio returns how many times the given p99 is over this baseline. Shared
// with AlertRuleEvaluator's "baseline_ratio" condition variable, so
// anomaly-based alert rules use the exact same notion of "baseline" as the
// PREDICTIVE_WARNING topology state.
func (b *serviceBaseline) ratio(currentP99Ms float64) float64 {
	if b == nil || b.ewmaP99Ms <= 0 {
		return 0
	}
	return currentP99Ms / b.ewmaP99Ms
}

// AnomalyDetector polls real per-service RED metrics (rate/error/duration)
// computed directly from ClickHouse trace data and flags services whose
// current p99 latency has drifted meaningfully above their own recent
// baseline. This replaces an earlier placeholder that generated
// rand.Float64() as fake latency for a single hardcoded service name —
// everything here now reads real ingested telemetry.
type AnomalyDetector struct {
	topoclient *client.TopologyClient
	metrics    *redMetricsClient
	baselines  map[string]*serviceBaseline
}

func NewAnomalyDetector(topoclient *client.TopologyClient) *AnomalyDetector {
	return &AnomalyDetector{
		topoclient: topoclient,
		metrics:    newRedMetricsClient(),
		baselines:  make(map[string]*serviceBaseline),
	}
}

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
	log.Printf("anomaly_detector: started, polling %s every %s", a.metrics.clickhouseURL, pollInterval)
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
	rows, err := a.metrics.fetch(ctx)
	if err != nil {
		log.Printf("anomaly_detector: failed to fetch service metrics: %v", err)
		return
	}

	for _, row := range rows {
		if row.Requests < minRequestsToConsider {
			continue
		}

		// Baselines and topology updates are per-tenant: a same-named service in
		// two tenants gets independent baselines and independent PREDICTIVE_WARNING state.
		key := baselineKey(row.Tenant, row.Service)
		baseline, ok := a.baselines[key]
		if !ok {
			// First time we've seen this service: seed the baseline with its
			// current p99 rather than comparing against zero.
			a.baselines[key] = &serviceBaseline{ewmaP99Ms: row.P99Ms, samples: 1}
			continue
		}

		// Compare against the *existing* baseline before folding this sample in,
		// so the update below doesn't chase its own tail.
		if baseline.samples >= minSamplesToWarn && baseline.ewmaP99Ms > 0 && row.P99Ms >= baseline.ewmaP99Ms*warnMultiplier {
			log.Printf("anomaly_detector: ⚠️ %s/%s p99 latency %.1fms is %.1fx its baseline (%.1fms) — PREDICTIVE_WARNING",
				row.tenantOrDefault(), row.Service, row.P99Ms, row.P99Ms/baseline.ewmaP99Ms, baseline.ewmaP99Ms)

			if err := a.topoclient.UpdateServiceState(ctx, row.tenantOrDefault(), row.Service, "PREDICTIVE_WARNING"); err != nil {
				log.Printf("anomaly_detector: failed to update predictive state for %s: %v", row.Service, err)
			}
		}

		baseline.ewmaP99Ms = (ewmaAlpha * row.P99Ms) + ((1 - ewmaAlpha) * baseline.ewmaP99Ms)
		baseline.samples++
	}
}
