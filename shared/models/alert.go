package models

import "time"

// Alert represents a triggered alert derived from a log event.
type Alert struct {
	ID          string    `json:"id" db:"id"`
	LogEntryID  string    `json:"log_entry_id" db:"log_entry_id"`
	ServiceName string    `json:"service" db:"service_name"`
	Level       LogLevel  `json:"level" db:"level"`
	Message     string    `json:"message" db:"message"`
	TraceID     string    `json:"trace_id,omitempty" db:"trace_id"`
	TriggeredAt time.Time `json:"triggered_at" db:"triggered_at"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// AlertQueryParams holds filter/pagination options for querying alerts.
type AlertQueryParams struct {
	ServiceName string   `form:"service"`
	Level       LogLevel `form:"level"`
	From        string   `form:"from"` // RFC3339
	To          string   `form:"to"`   // RFC3339
	Page        int      `form:"page"`
	PageSize    int      `form:"page_size"`
}
