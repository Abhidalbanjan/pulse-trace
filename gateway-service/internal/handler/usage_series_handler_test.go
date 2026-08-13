package handler

import "testing"

func TestProjectOverage(t *testing.T) {
	// Halfway through a 30-day period, 6M used → projects to 12M.
	p := projectOverage(6_000_000, 20_000_000, 15, 30)
	if p.Projected != 12_000_000 {
		t.Errorf("projected = %d, want 12,000,000", p.Projected)
	}
	if p.WillExceed {
		t.Errorf("12M projected under a 20M limit should not exceed")
	}

	// Same elapsed fraction but a small limit → projected breach.
	p = projectOverage(6_000_000, 10_000_000, 15, 30)
	if !p.WillExceed {
		t.Errorf("12M projected over a 10M limit should exceed: %+v", p)
	}
	if p.UsedPct != 60 {
		t.Errorf("used_pct = %.1f, want 60", p.UsedPct)
	}
}

func TestProjectOverage_NoElapsedTimeIsSafe(t *testing.T) {
	// Before any time elapses, projection is just the current total (no div-by-0).
	p := projectOverage(500, 1000, 0, 30)
	if p.Projected != 500 {
		t.Errorf("projected = %d, want 500 (no extrapolation)", p.Projected)
	}
	// Zero period days must not panic/divide-by-zero either.
	p = projectOverage(500, 1000, 5, 0)
	if p.Projected != 500 {
		t.Errorf("zero period should yield current total, got %d", p.Projected)
	}
}

func TestProjectOverage_UnlimitedPlan(t *testing.T) {
	// A zero/negative limit means unlimited → never exceeds, no pct.
	p := projectOverage(999_999_999, 0, 15, 30)
	if p.WillExceed || p.UsedPct != 0 || p.ProjectPct != 0 {
		t.Errorf("unlimited plan should never exceed and report 0 pct: %+v", p)
	}
}
