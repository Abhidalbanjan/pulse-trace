package models

import "time"

// IncidentStatus represents the lifecycle state of an incident.
type IncidentStatus string

const (
	IncidentStatusOpen     IncidentStatus = "OPEN"
	IncidentStatusResolved IncidentStatus = "RESOLVED"
)

// Incident groups one or more related alerts into a single actionable event.
// The correlation engine creates incidents by clustering alerts that share
// a service dependency graph within a sliding time window.
type Incident struct {
	ID           string         `json:"id" db:"id"`
	Title        string         `json:"title" db:"title"`
	RootCause    string         `json:"root_cause" db:"root_cause"`
	Status       IncidentStatus `json:"status" db:"status"`
	Severity     LogLevel       `json:"severity" db:"severity"` // highest alert level in the group
	ServiceNames []string       `json:"services" db:"-"`        // populated from incident_alerts join
	AlertCount   int            `json:"alert_count" db:"alert_count"`
	StartedAt    time.Time      `json:"started_at" db:"started_at"`
	ResolvedAt   *time.Time     `json:"resolved_at,omitempty" db:"resolved_at"`
	CreatedAt    time.Time      `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at" db:"updated_at"`
}

// IncidentAlert is the join record linking an alert to an incident.
type IncidentAlert struct {
	IncidentID  string    `json:"incident_id" db:"incident_id"`
	AlertID     string    `json:"alert_id" db:"alert_id"`
	ServiceName string    `json:"service" db:"service_name"`
	Level       LogLevel  `json:"level" db:"level"`
	Message     string    `json:"message" db:"message"`
	TriggeredAt time.Time `json:"triggered_at" db:"triggered_at"`
}

// IncidentTimelineEvent is a single entry in an incident's timeline.
type IncidentTimelineEvent struct {
	At          time.Time `json:"at"`
	EventType   string    `json:"event_type"` // alert_triggered, incident_opened, incident_resolved
	ServiceName string    `json:"service,omitempty"`
	Level       string    `json:"level,omitempty"`
	Description string    `json:"description"`
}

// IncidentQueryParams holds filter/pagination options for querying incidents.
type IncidentQueryParams struct {
	Status   string `form:"status"`
	Severity string `form:"severity"`
	Service  string `form:"service"`
	From     string `form:"from"`
	To       string `form:"to"`
	Page     int    `form:"page"`
	PageSize int    `form:"page_size"`
}
