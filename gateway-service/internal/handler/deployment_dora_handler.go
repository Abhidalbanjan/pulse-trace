package handler

// DORA metrics (Deploy Gates · E2).
//
// The four Accelerate/DORA metrics turn raw deploy + incident history into the
// shape a leader asks about: how often we ship, and how safely. Deployments live
// in gateway's Postgres and incidents in correlation's — but both share one
// database, so we can join them here. Lead time needs commit timestamps we don't
// capture, so it's intentionally omitted rather than faked. The math is pure and
// unit-tested; the handler just supplies the two windowed slices.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// doraFailureWindow: an incident on a deployed service that starts within this
// window after the deploy is attributed to it (a "change failure").
const doraFailureWindow = 2 * time.Hour

type doraDeploy struct {
	Service    string
	DeployedAt time.Time
}

type doraIncident struct {
	Services   []string
	StartedAt  time.Time
	ResolvedAt *time.Time
}

// DORAMetrics is the computed scorecard.
type DORAMetrics struct {
	WindowDays           float64 `json:"window_days"`
	TotalDeploys         int     `json:"total_deploys"`
	DeployFreqPerDay     float64 `json:"deploy_frequency_per_day"`
	DeployFreqRating     string  `json:"deploy_frequency_rating"`
	FailedDeploys        int     `json:"failed_deploys"`
	ChangeFailureRatePct float64 `json:"change_failure_rate_pct"`
	ChangeFailureRating  string  `json:"change_failure_rating"`
	ResolvedIncidents    int     `json:"resolved_incidents"`
	MTTRMinutes          float64 `json:"mttr_minutes"`
	MTTRRating           string  `json:"mttr_rating"`
}

// deployCausedIncident reports whether any incident on the deploy's service
// started within the failure window after it. Pure.
func deployCausedIncident(d doraDeploy, incidents []doraIncident, window time.Duration) bool {
	for _, inc := range incidents {
		if !inc.StartedAt.After(d.DeployedAt) || inc.StartedAt.Sub(d.DeployedAt) > window {
			continue
		}
		for _, s := range inc.Services {
			if s == d.Service {
				return true
			}
		}
	}
	return false
}

// DORA rating bands (simplified Accelerate thresholds).
func rateDeployFreq(perDay float64) string {
	switch {
	case perDay >= 1:
		return "elite"
	case perDay >= 1.0/7:
		return "high"
	case perDay >= 1.0/30:
		return "medium"
	default:
		return "low"
	}
}

func rateChangeFailure(pct float64) string {
	switch {
	case pct <= 15:
		return "elite"
	case pct <= 30:
		return "high"
	case pct <= 45:
		return "medium"
	default:
		return "low"
	}
}

func rateMTTR(minutes float64, resolved int) string {
	if resolved == 0 {
		return "n/a"
	}
	switch {
	case minutes < 60:
		return "elite"
	case minutes < 24*60:
		return "high"
	case minutes < 7*24*60:
		return "medium"
	default:
		return "low"
	}
}

// computeDORA derives the DORA scorecard from windowed deploys + incidents. Pure:
// no DB, no clock. Change-failure rate links deploys to incidents by service +
// time proximity; MTTR averages resolved-incident restore times.
func computeDORA(deploys []doraDeploy, incidents []doraIncident, windowDays float64, failureWindow time.Duration) DORAMetrics {
	m := DORAMetrics{WindowDays: windowDays, TotalDeploys: len(deploys)}

	if windowDays > 0 {
		m.DeployFreqPerDay = float64(len(deploys)) / windowDays
	}
	m.DeployFreqRating = rateDeployFreq(m.DeployFreqPerDay)

	for _, d := range deploys {
		if deployCausedIncident(d, incidents, failureWindow) {
			m.FailedDeploys++
		}
	}
	if len(deploys) > 0 {
		m.ChangeFailureRatePct = float64(m.FailedDeploys) / float64(len(deploys)) * 100
	}
	m.ChangeFailureRating = rateChangeFailure(m.ChangeFailureRatePct)

	var total time.Duration
	for _, inc := range incidents {
		if inc.ResolvedAt != nil && inc.ResolvedAt.After(inc.StartedAt) {
			total += inc.ResolvedAt.Sub(inc.StartedAt)
			m.ResolvedIncidents++
		}
	}
	if m.ResolvedIncidents > 0 {
		m.MTTRMinutes = total.Minutes() / float64(m.ResolvedIncidents)
	}
	m.MTTRRating = rateMTTR(m.MTTRMinutes, m.ResolvedIncidents)

	return m
}

// GetDORA returns the DORA scorecard over an optional [from,to] window (default
// last 30 days).
//
//	GET /api/v1/deployments/dora?from=&to=
func (h *DeploymentHandler) GetDORA(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.db == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	now := time.Now().UTC()
	to := now
	if t, ok := parseDeployTime(r.URL.Query().Get("to")); ok {
		to = t
	}
	from := to.AddDate(0, 0, -30)
	if t, ok := parseDeployTime(r.URL.Query().Get("from")); ok && t.Before(to) {
		from = t
	}
	windowDays := to.Sub(from).Hours() / 24
	tenant := tenantFromRequest(r)

	deploys, err := h.queryDoraDeploys(tenant, from, to)
	if err != nil {
		http.Error(w, "failed to load deployments", http.StatusInternalServerError)
		return
	}
	// Include incidents up to the failure window past `to` so a deploy near the
	// window's end can still be linked to the incident it caused.
	incidents, err := h.queryDoraIncidents(tenant, from, to.Add(doraFailureWindow))
	if err != nil {
		http.Error(w, "failed to load incidents", http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(computeDORA(deploys, incidents, windowDays, doraFailureWindow))
}

func (h *DeploymentHandler) queryDoraDeploys(tenant string, from, to time.Time) ([]doraDeploy, error) {
	rows, err := h.db.Query(
		`SELECT service, deployed_at FROM deployments WHERE tenant_id = $1 AND deployed_at >= $2 AND deployed_at <= $3`,
		tenant, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []doraDeploy{}
	for rows.Next() {
		var d doraDeploy
		if err := rows.Scan(&d.Service, &d.DeployedAt); err != nil {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func (h *DeploymentHandler) queryDoraIncidents(tenant string, from, to time.Time) ([]doraIncident, error) {
	// incidents.tenant_id + services from the incident_alerts join (an incident can
	// span services). string_agg avoids a Postgres-array scan dependency.
	rows, err := h.db.Query(`
		SELECT i.started_at, i.resolved_at, COALESCE(string_agg(DISTINCT ia.service_name, ','), '')
		FROM incidents i
		LEFT JOIN incident_alerts ia ON ia.incident_id = i.id
		WHERE i.tenant_id = $1 AND i.started_at >= $2 AND i.started_at <= $3
		GROUP BY i.id, i.started_at, i.resolved_at`,
		tenant, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []doraIncident{}
	for rows.Next() {
		var startedAt time.Time
		var resolvedAt sql.NullTime
		var services string
		if err := rows.Scan(&startedAt, &resolvedAt, &services); err != nil {
			continue
		}
		inc := doraIncident{StartedAt: startedAt}
		if resolvedAt.Valid {
			t := resolvedAt.Time
			inc.ResolvedAt = &t
		}
		if services != "" {
			inc.Services = strings.Split(services, ",")
		}
		out = append(out, inc)
	}
	return out, nil
}
