package models

import "time"

// SLODefinition represents a configurable SLO target for a service.
type SLODefinition struct {
	ID          string    `json:"id" db:"id"`
	ServiceName string    `json:"service_name" db:"service_name"`
	SLOTarget   float64   `json:"slo_target" db:"slo_target"`     // e.g. 99.9
	SLIType     string    `json:"sli_type" db:"sli_type"`         // "availability" or "latency"
	WindowDays  int       `json:"window_days" db:"window_days"`   // e.g. 30
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}

// SLOSnapshot represents a periodic SLI measurement taken by the background worker.
type SLOSnapshot struct {
	ID          int64     `json:"id" db:"id"`
	ServiceName string    `json:"service_name" db:"service_name"`
	SLIValue    float64   `json:"sli_value" db:"sli_value"`       // e.g. 99.8523
	TotalEvents int64     `json:"total_events" db:"total_events"`
	ErrorEvents int64     `json:"error_events" db:"error_events"`
	WindowStart time.Time `json:"window_start" db:"window_start"`
	WindowEnd   time.Time `json:"window_end" db:"window_end"`
	SnapshotAt  time.Time `json:"snapshot_at" db:"snapshot_at"`
}

// SLOBudgetAlert represents a record of when an error budget burn rate threshold was breached.
type SLOBudgetAlert struct {
	ID                 string    `json:"id" db:"id"`
	ServiceName        string    `json:"service_name" db:"service_name"`
	BurnRate           float64   `json:"burn_rate" db:"burn_rate"`
	BudgetRemainingPct float64   `json:"budget_remaining_pct" db:"budget_remaining_pct"`
	Severity           string    `json:"severity" db:"severity"`
	Message            string    `json:"message" db:"message"`
	TriggeredAt        time.Time `json:"triggered_at" db:"triggered_at"`
}

// SLODashboardItem is the computed response struct sent to the frontend.
// It combines the definition, current SLI, error budget, burn rate, and trend.
type SLODashboardItem struct {
	Definition        SLODefinition  `json:"definition"`
	CurrentSLI        float64        `json:"current_sli"`         // e.g. 99.85
	TotalEvents       int64          `json:"total_events"`
	ErrorEvents       int64          `json:"error_events"`
	BudgetTotalMin    float64        `json:"budget_total_min"`    // total error budget in minutes
	BudgetUsedMin     float64        `json:"budget_used_min"`     // budget consumed in minutes
	BudgetRemainingPct float64       `json:"budget_remaining_pct"` // 0-100
	BurnRate          float64        `json:"burn_rate"`           // multiplier (1.0 = on-track)
	Status            string         `json:"status"`              // "healthy", "warning", "critical"
	Trend             []SLOTrendPoint `json:"trend"`              // time-series for sparklines

	// Error-budget-burn forecast (SLOs · E4). ForecastBurning is true only when
	// the recent budget trajectory is actually declining; when true,
	// ForecastExhaustAt is the projected run-out timestamp and ForecastDaysLeft
	// the days until then. When false (flat or improving budget), the projection
	// fields are omitted — there is no meaningful exhaustion date.
	ForecastBurning   bool       `json:"forecast_burning"`
	ForecastExhaustAt *time.Time `json:"forecast_exhaust_at,omitempty"`
	ForecastDaysLeft  float64    `json:"forecast_days_left,omitempty"`
}

// SLOTrendPoint is a single data point in the SLI trend sparkline.
type SLOTrendPoint struct {
	At       time.Time `json:"at"`
	SLIValue float64   `json:"sli_value"`
}

// CreateSLORequest is the payload accepted by the SLO definition create/update endpoint.
type CreateSLORequest struct {
	ServiceName string  `json:"service_name" validate:"required"`
	SLOTarget   float64 `json:"slo_target" validate:"required"`    // e.g. 99.9
	SLIType     string  `json:"sli_type"`                          // defaults to "availability"
	WindowDays  int     `json:"window_days"`                       // defaults to 30
}
