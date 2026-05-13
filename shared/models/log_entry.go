package models

import "time"

// LogLevel represents the severity of a log entry.
type LogLevel string

const (
	LogLevelDebug   LogLevel = "DEBUG"
	LogLevelInfo    LogLevel = "INFO"
	LogLevelWarning LogLevel = "WARNING"
	LogLevelError   LogLevel = "ERROR"
	LogLevelFatal   LogLevel = "FATAL"
)

// LogEntry represents a structured log event emitted by a microservice.
type LogEntry struct {
	ID          string    `json:"id" db:"id"`
	ServiceName string    `json:"service" db:"service_name"`
	Level       LogLevel  `json:"level" db:"level"`
	Message     string    `json:"message" db:"message"`
	TraceID     string    `json:"trace_id,omitempty" db:"trace_id"`
	SpanID      string    `json:"span_id,omitempty" db:"span_id"`
	Metadata    string    `json:"metadata,omitempty" db:"metadata"` // JSON blob for extra fields
	Timestamp   time.Time `json:"timestamp" db:"timestamp"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// CreateLogRequest is the payload accepted by the log ingestion endpoint.
type CreateLogRequest struct {
	ServiceName string            `json:"service" validate:"required"`
	Level       LogLevel          `json:"level" validate:"required,oneof=DEBUG INFO WARNING ERROR FATAL"`
	Message     string            `json:"message" validate:"required"`
	TraceID     string            `json:"trace_id,omitempty"`
	SpanID      string            `json:"span_id,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Timestamp   *time.Time        `json:"timestamp,omitempty"` // defaults to now if omitted
}

// LogQueryParams holds filter/pagination options for querying logs.
type LogQueryParams struct {
	ServiceName string   `form:"service"`
	Level       LogLevel `form:"level"`
	TraceID     string   `form:"trace_id"`
	From        string   `form:"from"` // RFC3339
	To          string   `form:"to"`   // RFC3339
	Page        int      `form:"page"`
	PageSize    int      `form:"page_size"`
}
