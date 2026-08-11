package engine

import (
	"time"

	"github.com/pulsetrace/shared/models"
)

// forecastHorizonDays caps how far out a projection is considered meaningful. A
// budget bleeding so slowly that it runs out in, say, 30 years is — for an
// operator's purposes — not burning; reporting a run-out date that far away is
// noise, and projecting it risks time overflow. Beyond this horizon we report
// "not burning."
const forecastHorizonDays = 365.0

// ForecastBudgetExhaustion projects when a service's error budget will be fully
// consumed, from the recent budget-remaining trajectory (SLOs · E4).
//
// It is a pure function (no I/O) so the projection math is unit-testable without
// a database. It converts the SLI trend into a budget-remaining series using the
// SLO target, fits a least-squares line over time, and — only when the budget is
// genuinely declining — projects from the *current* remaining budget to zero at
// the fitted rate.
//
// Returns burning=false (and no date) when:
//   - the SLO leaves no budget to burn (target ≥ 100%),
//   - there aren't at least two distinct-time points to fit a slope,
//   - the budget is flat or improving (slope ≥ 0), or
//   - the projected run-out is beyond forecastHorizonDays (negligibly slow).
//
// When burning, exhaustAt is now + timeToZero and daysLeft is that span in days
// (0 if the budget is already exhausted).
func ForecastBudgetExhaustion(trend []models.SLOTrendPoint, sloTarget, currentBudgetRemainingPct float64, now time.Time) (exhaustAt *time.Time, daysLeft float64, burning bool) {
	errorBudgetTotal := 100.0 - sloTarget
	if errorBudgetTotal <= 0 {
		return nil, 0, false // a 100% target has no budget to project
	}

	// Derive the budget-remaining series (%) from the SLI trend. x is seconds so
	// the fitted slope is percentage-points per second.
	xs := make([]float64, 0, len(trend))
	ys := make([]float64, 0, len(trend))
	for _, p := range trend {
		rem := budgetRemainingFromSLI(p.SLIValue, errorBudgetTotal)
		xs = append(xs, float64(p.At.Unix()))
		ys = append(ys, rem)
	}

	slope, ok := leastSquaresSlope(xs, ys)
	if !ok {
		return nil, 0, false // not enough distinct points to fit a trend
	}
	if slope >= 0 {
		return nil, 0, false // budget flat or recovering — nothing to project
	}

	remaining := clampPct(currentBudgetRemainingPct)
	if remaining <= 0 {
		// Already exhausted; the run-out is "now".
		at := now
		return &at, 0, true
	}

	// slope is negative (pct-points per second); time to reach zero:
	secondsToZero := remaining / (-slope)
	daysLeft = secondsToZero / 86400.0
	if daysLeft > forecastHorizonDays {
		return nil, 0, false // burning too slowly to be actionable
	}

	at := now.Add(time.Duration(secondsToZero * float64(time.Second)))
	return &at, daysLeft, true
}

// budgetRemainingFromSLI converts an SLI value into remaining-budget percent,
// clamped to [0,100]. Mirrors the dashboard's own budget math so the forecast
// and the displayed budget agree.
func budgetRemainingFromSLI(sli, errorBudgetTotal float64) float64 {
	if errorBudgetTotal <= 0 {
		return 100
	}
	used := 100.0 - sli
	return clampPct(((errorBudgetTotal - used) / errorBudgetTotal) * 100.0)
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// leastSquaresSlope fits y = a + b·x and returns b (the slope). ok is false when
// there are fewer than two points or the x values have zero variance (all the
// same timestamp), where a slope is undefined.
func leastSquaresSlope(xs, ys []float64) (slope float64, ok bool) {
	n := len(xs)
	if n < 2 || n != len(ys) {
		return 0, false
	}
	var sumX, sumY, sumXY, sumXX float64
	for i := 0; i < n; i++ {
		sumX += xs[i]
		sumY += ys[i]
		sumXY += xs[i] * ys[i]
		sumXX += xs[i] * xs[i]
	}
	nf := float64(n)
	denom := nf*sumXX - sumX*sumX
	if denom == 0 {
		return 0, false // no variance in x
	}
	return (nf*sumXY - sumX*sumY) / denom, true
}
