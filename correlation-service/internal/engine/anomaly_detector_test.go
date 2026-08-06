package engine

import (
	"strings"
	"testing"

	"github.com/pulsetrace/correlation-service/internal/repository"
)

// baseline with enough samples to be trusted, and modest healthy values.
func healthyBaseline() *serviceBaseline {
	return &serviceBaseline{
		ewmaP99Ms:     100,
		ewmaErrorRate: 1.0, // 1%
		ewmaRequests:  1000,
		samples:       minSamplesToWarn,
	}
}

func TestDetectAnomalies(t *testing.T) {
	cases := []struct {
		name     string
		row      serviceRow
		wantHit  string // substring the reason must contain; "" = expect no anomaly
	}{
		{
			name: "healthy",
			row:  serviceRow{Requests: 1000, Errors: 10, P99Ms: 110}, // 1% errors, p99 near baseline
		},
		{
			name:    "latency spike",
			row:     serviceRow{Requests: 1000, Errors: 10, P99Ms: 200}, // 2x baseline
			wantHit: "p99 latency",
		},
		{
			name:    "error-rate jump",
			row:     serviceRow{Requests: 1000, Errors: 120, P99Ms: 110}, // 12% >> 1% baseline + floor
			wantHit: "error rate",
		},
		{
			name:    "throughput drop",
			row:     serviceRow{Requests: 200, Errors: 2, P99Ms: 110}, // 200 <= 0.4*1000
			wantHit: "throughput dropped",
		},
		{
			// A small error-rate wobble that clears neither the floor nor the jump
			// must not fire.
			name: "minor error wobble ignored",
			row:  serviceRow{Requests: 1000, Errors: 30, P99Ms: 110}, // 3% < 5% floor
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reasons := detectAnomalies(healthyBaseline(), c.row, repository.DefaultAnomalyConfig())
			joined := strings.Join(reasons, "; ")
			if c.wantHit == "" {
				if len(reasons) != 0 {
					t.Errorf("expected no anomaly, got %q", joined)
				}
				return
			}
			if !strings.Contains(joined, c.wantHit) {
				t.Errorf("expected an anomaly containing %q, got %q", c.wantHit, joined)
			}
		})
	}
}

// TestDetectAnomaliesRespectsConfig proves the per-tenant tuning (F14) actually
// changes the verdict: the same 1.5×-baseline p99 is healthy under the default
// 1.6× multiplier but anomalous under a stricter 1.4×.
func TestDetectAnomaliesRespectsConfig(t *testing.T) {
	b := healthyBaseline() // ewmaP99Ms = 100
	row := serviceRow{Requests: 1000, Errors: 10, P99Ms: 150} // 1.5× baseline, 1% errors

	if r := detectAnomalies(b, row, repository.DefaultAnomalyConfig()); len(r) != 0 {
		t.Errorf("1.5× baseline under the default 1.6× multiplier must not fire, got %q", strings.Join(r, "; "))
	}

	strict := repository.DefaultAnomalyConfig()
	strict.P99Multiplier = 1.4
	if r := detectAnomalies(b, row, strict); len(r) == 0 {
		t.Error("1.5× baseline under a stricter 1.4× multiplier must fire")
	}
}

// A cold baseline (too few samples) must never fire, however extreme the input.
func TestDetectAnomaliesRequiresBaselineHistory(t *testing.T) {
	cold := &serviceBaseline{ewmaP99Ms: 100, ewmaErrorRate: 1, ewmaRequests: 1000, samples: 1}
	if r := detectAnomalies(cold, serviceRow{Requests: 5000, Errors: 5000, P99Ms: 9999}, repository.DefaultAnomalyConfig()); len(r) != 0 {
		t.Errorf("cold baseline should not fire, got %q", strings.Join(r, "; "))
	}
}
