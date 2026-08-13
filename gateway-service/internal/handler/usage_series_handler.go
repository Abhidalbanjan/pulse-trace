package handler

// Usage & quota dashboard (Settings · E1).
//
// GetUsage gives a single month-to-date total per signal; this adds the daily
// series plus a run-rate projection so a tenant can see a quota breach coming
// (and upgrade) rather than discovering it when ingestion starts getting
// throttled. The projection math is pure so it's unit-tested without a DB.

import (
	"database/sql"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/pulsetrace/gateway-service/internal/quota"
)

// usageSignals is the fixed set of metered signals, in display order.
var usageSignals = []string{"logs", "traces", "metrics", "rum"}

// OverageProjection is a signal's run-rate forecast for the billing period.
type OverageProjection struct {
	Projected  int64   `json:"projected"`   // linear end-of-period estimate
	Limit      int64   `json:"limit"`       // plan quota (0 = unlimited)
	UsedPct    float64 `json:"used_pct"`    // current cumulative vs limit
	ProjectPct float64 `json:"project_pct"` // projected vs limit
	WillExceed bool    `json:"will_exceed"` // projected to cross the limit before period end
}

// projectOverage forecasts end-of-period usage by linear extrapolation of the
// current cumulative total over the fraction of the period elapsed, and flags a
// projected breach. Pure: given the same inputs it always returns the same
// projection. A zero/negative limit means "unlimited" (never exceeds); before any
// time has elapsed the projection is just the current total (no divide-by-zero).
func projectOverage(cumulative, limit int64, elapsedDays, periodDays float64) OverageProjection {
	p := OverageProjection{Limit: limit, Projected: cumulative}

	frac := 0.0
	if periodDays > 0 {
		frac = elapsedDays / periodDays
	}
	if frac > 0 {
		p.Projected = int64(math.Round(float64(cumulative) / frac))
	}
	if limit > 0 {
		p.UsedPct = float64(cumulative) / float64(limit) * 100
		p.ProjectPct = float64(p.Projected) / float64(limit) * 100
		p.WillExceed = p.Projected > limit
	}
	return p
}

func limitForSignal(l quota.Limits, signal string) int64 {
	switch signal {
	case "traces":
		return l.Traces
	case "metrics":
		return l.Metrics
	case "logs":
		return l.Logs
	case "rum":
		return l.RUM
	}
	return 0
}

type usageDayPoint struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

type usageSignalReport struct {
	Signal     string            `json:"signal"`
	Total      int64             `json:"total"`
	Limit      int64             `json:"limit"`
	Series     []usageDayPoint   `json:"series"`
	Projection OverageProjection `json:"projection"`
}

// GetUsageSeries returns the per-signal daily usage series for the current
// billing month plus a run-rate overage projection against the tenant's plan.
//
//	GET /api/v1/usage/series
func (h *UsageHandler) GetUsageSeries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	tenant := tenantFromRequest(r)

	now := time.Now().UTC()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonth := periodStart.AddDate(0, 1, 0)
	periodDays := nextMonth.Sub(periodStart).Hours() / 24
	// Elapsed includes the current (partial) day so a run-rate on day 1 isn't
	// division by ~0; clamp to at least 1 day.
	elapsedDays := math.Max(1, now.Sub(periodStart).Hours()/24)

	// series[signal][day] = count
	series := map[string][]usageDayPoint{}
	totals := map[string]int64{}

	if h.db != nil {
		rows, err := h.db.Query(`
			SELECT signal, day::text, COALESCE(count, 0)
			FROM usage_daily
			WHERE tenant_id = $1 AND day >= $2 AND day < $3
			ORDER BY day ASC`, tenant, periodStart, nextMonth)
		if err != nil {
			log.Printf("usage/series: query failed: %v", err)
			http.Error(w, "failed to load usage", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var signal, day string
			var count int64
			if err := rows.Scan(&signal, &day, &count); err != nil {
				continue
			}
			series[signal] = append(series[signal], usageDayPoint{Day: day, Count: count})
			totals[signal] += count
		}
	}

	limits, _ := quota.LimitsForPlan(h.planFor(tenant))

	reports := make([]usageSignalReport, 0, len(usageSignals))
	for _, sig := range usageSignals {
		limit := limitForSignal(limits, sig)
		pts := series[sig]
		if pts == nil {
			pts = []usageDayPoint{}
		}
		reports = append(reports, usageSignalReport{
			Signal:     sig,
			Total:      totals[sig],
			Limit:      limit,
			Series:     pts,
			Projection: projectOverage(totals[sig], limit, elapsedDays, periodDays),
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tenant_id": tenant,
		"plan":      h.planFor(tenant),
		"period": map[string]interface{}{
			"from":         periodStart.Format("2006-01-02"),
			"to":           nextMonth.AddDate(0, 0, -1).Format("2006-01-02"),
			"days":         int(math.Round(periodDays)),
			"elapsed_days": int(math.Round(elapsedDays)),
		},
		"signals": reports,
	})
}

// planFor resolves the tenant's billing plan, defaulting to "free" when unknown
// so limits are always resolvable.
func (h *UsageHandler) planFor(tenant string) string {
	if h.db == nil {
		return "free"
	}
	var plan string
	if err := h.db.QueryRow("SELECT plan FROM tenants WHERE id = $1", tenant).Scan(&plan); err != nil {
		if err != sql.ErrNoRows {
			log.Printf("usage/series: plan lookup failed: %v", err)
		}
		return "free"
	}
	if plan == "" {
		return "free"
	}
	return plan
}
