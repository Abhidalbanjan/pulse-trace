package handler

import "testing"

func TestClassifyUptime(t *testing.T) {
	cases := []struct {
		name           string
		total, success int64
		wantStatus     string
	}{
		{"no data", 0, 0, "no-data"},
		{"fully up", 100, 100, "up"},
		{"up within noise threshold", 1000, 996, "up"}, // 99.6% >= 99.5
		{"degraded just under threshold", 1000, 994, "degraded"},
		{"degraded partial", 10, 5, "degraded"},
		{"down", 10, 0, "down"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, status := classifyUptime(c.total, c.success)
			if status != c.wantStatus {
				t.Errorf("classifyUptime(%d,%d) status = %q, want %q", c.total, c.success, status, c.wantStatus)
			}
		})
	}
}

func TestComputeUptime(t *testing.T) {
	raw := []uptimeRawBucket{
		{Start: "b1", Total: 10, Success: 10}, // up
		{Start: "b2", Total: 10, Success: 5},  // degraded
		{Start: "b3", Total: 0, Success: 0},   // no-data (must not skew SLA)
		{Start: "b4", Total: 10, Success: 0},  // down
	}
	got := computeUptime("checkout-flow", raw)

	if got.Target != "checkout-flow" {
		t.Errorf("target = %q", got.Target)
	}
	if len(got.Buckets) != 4 {
		t.Fatalf("expected 4 buckets (no-data preserved), got %d", len(got.Buckets))
	}
	if got.Buckets[2].Status != "no-data" {
		t.Errorf("empty bucket should be no-data, got %q", got.Buckets[2].Status)
	}
	// Overall = 15 success / 30 checks = 50% (the empty bucket contributes nothing).
	if got.Total != 30 || got.Success != 15 {
		t.Errorf("totals = %d/%d, want 30/15", got.Success, got.Total)
	}
	if got.UptimePct != 50 {
		t.Errorf("uptime = %.2f, want 50", got.UptimePct)
	}
}

func TestComputeUptime_Empty(t *testing.T) {
	got := computeUptime("x", nil)
	if got.UptimePct != 0 || got.Total != 0 || len(got.Buckets) != 0 {
		t.Errorf("empty input should yield zero summary, got %+v", got)
	}
}

func TestUptimeBucketSeconds(t *testing.T) {
	// 24h / 48 = 1800s buckets.
	if got := uptimeBucketSeconds(24*3600, 48); got != 1800 {
		t.Errorf("24h/48 = %d, want 1800", got)
	}
	// Short window floors at 60s.
	if got := uptimeBucketSeconds(600, 48); got != 60 {
		t.Errorf("short window should floor at 60, got %d", got)
	}
	// Guard against divide-by-zero on a bad target count.
	if got := uptimeBucketSeconds(3600, 0); got != 3600 {
		t.Errorf("targetBuckets<1 should treat as 1, got %d", got)
	}
}
