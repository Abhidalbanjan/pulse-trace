package engine

import (
	"math"
	"testing"
	"time"

	"github.com/pulsetrace/shared/models"
)

// trendFromRemaining builds an SLI trend that yields the given budget-remaining
// percentages for a target, one point per day ending at `end`. It is the
// inverse of budgetRemainingFromSLI, so tests can express trajectories in the
// budget terms operators think in.
func trendFromRemaining(end time.Time, sloTarget float64, rems []float64) []models.SLOTrendPoint {
	errorBudgetTotal := 100.0 - sloTarget
	pts := make([]models.SLOTrendPoint, len(rems))
	for i, rem := range rems {
		used := errorBudgetTotal * (1 - rem/100.0)
		sli := 100.0 - used
		at := end.AddDate(0, 0, -(len(rems) - 1 - i))
		pts[i] = models.SLOTrendPoint{At: at, SLIValue: sli}
	}
	return pts
}

func TestForecast_DecliningBudgetProjectsRunOut(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	// 90% → 60% → 30% over two days: losing 30 pts/day. From 30% remaining, the
	// budget runs out in ~1 day.
	trend := trendFromRemaining(now, 99.9, []float64{90, 60, 30})

	at, daysLeft, burning := ForecastBudgetExhaustion(trend, 99.9, 30, now)
	if !burning {
		t.Fatal("declining budget must be reported as burning")
	}
	if at == nil {
		t.Fatal("expected an exhaustion timestamp")
	}
	if math.Abs(daysLeft-1.0) > 0.05 {
		t.Fatalf("expected ~1 day left, got %.3f", daysLeft)
	}
	if at.Before(now) {
		t.Fatal("run-out date must be in the future for a non-exhausted budget")
	}
}

func TestForecast_ImprovingBudgetNotBurning(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	trend := trendFromRemaining(now, 99.9, []float64{30, 60, 90}) // recovering
	if _, _, burning := ForecastBudgetExhaustion(trend, 99.9, 90, now); burning {
		t.Fatal("a recovering budget must not be reported as burning")
	}
}

func TestForecast_FlatBudgetNotBurning(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	trend := trendFromRemaining(now, 99.9, []float64{50, 50, 50})
	if _, _, burning := ForecastBudgetExhaustion(trend, 99.9, 50, now); burning {
		t.Fatal("a flat budget must not be reported as burning")
	}
}

func TestForecast_AlreadyExhausted(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	trend := trendFromRemaining(now, 99.9, []float64{40, 20, 0})
	at, daysLeft, burning := ForecastBudgetExhaustion(trend, 99.9, 0, now)
	if !burning || at == nil {
		t.Fatal("an exhausted-but-declining budget should report burning with a run-out")
	}
	if daysLeft != 0 || !at.Equal(now) {
		t.Fatalf("exhausted budget should run out now, got daysLeft=%.3f at=%v", daysLeft, at)
	}
}

func TestForecast_NoBudgetWhenTargetIs100(t *testing.T) {
	now := time.Now().UTC()
	trend := []models.SLOTrendPoint{{At: now.AddDate(0, 0, -1), SLIValue: 99}, {At: now, SLIValue: 98}}
	if _, _, burning := ForecastBudgetExhaustion(trend, 100, 0, now); burning {
		t.Fatal("a 100% target has no budget to burn")
	}
}

func TestForecast_TooFewPoints(t *testing.T) {
	now := time.Now().UTC()
	if _, _, burning := ForecastBudgetExhaustion([]models.SLOTrendPoint{{At: now, SLIValue: 99.9}}, 99.9, 50, now); burning {
		t.Fatal("a single point cannot yield a slope")
	}
	if _, _, burning := ForecastBudgetExhaustion(nil, 99.9, 50, now); burning {
		t.Fatal("no trend cannot be burning")
	}
}

func TestForecast_NegligibleBurnBeyondHorizon(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	// Barely declining: 100% → 99.999% over 2 days. Run-out is centuries away →
	// treated as not burning.
	trend := trendFromRemaining(now, 99.9, []float64{100, 99.9995, 99.999})
	if _, _, burning := ForecastBudgetExhaustion(trend, 99.9, 99.999, now); burning {
		t.Fatal("a negligibly-slow burn beyond the horizon must not be reported as burning")
	}
}

func TestLeastSquaresSlope_ZeroVarianceRejected(t *testing.T) {
	// All the same x (timestamp) → slope undefined.
	if _, ok := leastSquaresSlope([]float64{5, 5, 5}, []float64{1, 2, 3}); ok {
		t.Fatal("zero x-variance must be rejected")
	}
	if slope, ok := leastSquaresSlope([]float64{0, 1, 2}, []float64{0, 2, 4}); !ok || math.Abs(slope-2) > 1e-9 {
		t.Fatalf("expected slope 2, got %.4f ok=%v", slope, ok)
	}
}
