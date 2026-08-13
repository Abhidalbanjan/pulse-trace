package handler

import "testing"

func TestHealthScore(t *testing.T) {
	cases := []struct {
		name         string
		errPct, p99  float64
		budget       float64
		wantBand     string
		scoreAtLeast int
		scoreAtMost  int
	}{
		{"perfect", 0, 100, 500, "healthy", 100, 100},
		{"fast and clean under budget", 0, 400, 500, "healthy", 100, 100},
		{"mild latency over budget", 0, 750, 500, "degraded", 80, 80},   // 1.5x → -20
		{"moderate latency", 0, 1200, 500, "unhealthy", 44, 44},         // 2.4x → -56
		{"heavy latency", 0, 1500, 500, "critical", 20, 20},             // 3x → -80
		{"1pct errors", 1, 100, 500, "healthy", 90, 90},                 // -10
		{"5pct errors", 5, 100, 500, "unhealthy", 50, 50},               // -50
		{"catastrophic", 20, 3000, 500, "critical", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, band := healthScore(c.errPct, c.p99, c.budget)
			if s < c.scoreAtLeast || s > c.scoreAtMost {
				t.Errorf("score = %d, want in [%d,%d]", s, c.scoreAtLeast, c.scoreAtMost)
			}
			if band != c.wantBand {
				t.Errorf("band = %q, want %q (score %d)", band, c.wantBand, s)
			}
		})
	}
}

func TestHealthScore_ClampsAndBands(t *testing.T) {
	if s, _ := healthScore(0, 0, 500); s != 100 {
		t.Errorf("zero-load service should be 100, got %d", s)
	}
	// Never negative.
	if s, b := healthScore(1000, 99999, 500); s != 0 || b != "critical" {
		t.Errorf("worst case should clamp to 0/critical, got %d/%s", s, b)
	}
}
