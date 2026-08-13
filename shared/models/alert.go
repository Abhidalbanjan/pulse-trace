package models

import "time"

// Alert represents a triggered alert derived from a log event.
type Alert struct {
	TenantID    string    `json:"tenant_id,omitempty" db:"tenant_id"`
	ID          string    `json:"id" db:"id"`
	LogEntryID  string    `json:"log_entry_id" db:"log_entry_id"`
	ServiceName string    `json:"service" db:"service_name"`
	Level       LogLevel  `json:"level" db:"level"`
	Message     string    `json:"message" db:"message"`
	TraceID     string    `json:"trace_id,omitempty" db:"trace_id"`
	TriggeredAt time.Time `json:"triggered_at" db:"triggered_at"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// AlertGroup is a set of near-identical alerts collapsed into one row, so a
// storm of the same failure (e.g. a service throwing the same error hundreds of
// times) reads as one incident with a count rather than an unscannable stream.
// Members share a GroupKey (service + level + a fingerprint of the message with
// volatile bits like ids/numbers normalized out). Computed on read; not stored.
type AlertGroup struct {
	Key       string    `json:"key"`
	Service   string    `json:"service"`
	Level     LogLevel  `json:"level"`
	Sample    string    `json:"sample"`     // representative message (most recent instance)
	SampleID  string    `json:"sample_id"`  // id of that representative instance
	Count     int       `json:"count"`      // number of alerts in the group
	FirstSeen time.Time `json:"first_seen"` // earliest instance
	LastSeen  time.Time `json:"last_seen"`  // most recent instance
	Instances []*Alert  `json:"instances,omitempty"`
}

// AlertQueryParams holds filter/pagination options for querying alerts.
type AlertQueryParams struct {
	TenantID    string   `form:"tenant_id"`
	ServiceName string   `form:"service"`
	Level       LogLevel `form:"level"`
	From        string   `form:"from"` // RFC3339
	To          string   `form:"to"`   // RFC3339
	Page        int      `form:"page"`
	PageSize    int      `form:"page_size"`
}
