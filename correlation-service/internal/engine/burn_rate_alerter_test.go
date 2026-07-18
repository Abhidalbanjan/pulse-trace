package engine

import "testing"

// TestComputeBurnRate covers the Google SRE Handbook burn-rate math directly.
// This is the actual "is SLO alerting real" logic — burn rate determines
// whether a customer's error budget is being consumed fast enough to page
// someone, so getting these numbers wrong either misses real incidents or
// pages people for nothing.
func TestComputeBurnRate(t *testing.T) {
	tests := []struct {
		name           string
		currentSLI     float64
		sloTarget      float64
		windowDays     int
		wantOK         bool
		wantBurnRate   float64
		wantBudgetLeft float64
	}{
		{
			name:           "healthy service well within budget",
			currentSLI:     99.95,
			sloTarget:      99.9,
			windowDays:     30,
			wantOK:         true,
			wantBurnRate:   0.5, // 0.05% actual error / 0.1% allowed = 0.5x
			wantBudgetLeft: 50,  // used half the budget
		},
		{
			name:           "exactly meeting SLO consumes entire budget at 1x",
			currentSLI:     99.9,
			sloTarget:      99.9,
			windowDays:     30,
			wantOK:         true,
			wantBurnRate:   1.0,
			wantBudgetLeft: 0,
		},
		{
			name:           "severe degradation burns budget fast (CRITICAL territory)",
			currentSLI:     98.0,
			sloTarget:      99.9,
			windowDays:     30,
			wantOK:         true,
			wantBurnRate:   20.0, // 2.0% actual error / 0.1% allowed = 20x
			wantBudgetLeft: 0,    // clamped, budget is blown
		},
		{
			name:       "100% SLO target has no error budget to burn — must not divide by zero",
			currentSLI: 99.5,
			sloTarget:  100.0,
			windowDays: 30,
			wantOK:     false,
		},
		{
			name:       "zero window days must not divide by zero",
			currentSLI: 99.5,
			sloTarget:  99.9,
			windowDays: 0,
			wantOK:     false,
		},
		{
			name:       "negative window days is invalid, not just skipped silently",
			currentSLI: 99.5,
			sloTarget:  99.9,
			windowDays: -7,
			wantOK:     false,
		},
		{
			name:           "SLI better than target still computes a (low) burn rate, not zero",
			currentSLI:     99.99,
			sloTarget:      99.9,
			windowDays:     30,
			wantOK:         true,
			wantBurnRate:   0.1,
			wantBudgetLeft: 90,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			burnRate, budgetLeft, ok := computeBurnRate(tt.currentSLI, tt.sloTarget, tt.windowDays)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if !floatsClose(burnRate, tt.wantBurnRate) {
				t.Errorf("burnRate = %.4f, want %.4f", burnRate, tt.wantBurnRate)
			}
			if !floatsClose(budgetLeft, tt.wantBudgetLeft) {
				t.Errorf("budgetRemainingPct = %.4f, want %.4f", budgetLeft, tt.wantBudgetLeft)
			}
		})
	}
}

// TestBurnRateThresholdOrdering verifies DefaultBurnRateThresholds is ordered
// most-severe-first. Evaluate() breaks on the first match, so if this ordering
// is ever accidentally changed to least-severe-first, every incident would
// silently downgrade to INFO instead of paging on CRITICAL.
func TestBurnRateThresholdOrdering(t *testing.T) {
	if len(DefaultBurnRateThresholds) < 2 {
		t.Fatalf("expected multiple thresholds, got %d", len(DefaultBurnRateThresholds))
	}
	for i := 1; i < len(DefaultBurnRateThresholds); i++ {
		prev := DefaultBurnRateThresholds[i-1]
		curr := DefaultBurnRateThresholds[i]
		if curr.Multiplier >= prev.Multiplier {
			t.Errorf("thresholds must be strictly descending by multiplier: index %d (%.1fx, %s) is not less than index %d (%.1fx, %s)",
				i, curr.Multiplier, curr.Severity, i-1, prev.Multiplier, prev.Severity)
		}
	}
	if DefaultBurnRateThresholds[0].Severity != "CRITICAL" {
		t.Errorf("highest-multiplier threshold should be CRITICAL, got %s", DefaultBurnRateThresholds[0].Severity)
	}
}

func floatsClose(a, b float64) bool {
	const epsilon = 0.01
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < epsilon
}
