package models

import "time"

// NotificationChannel identifies the delivery channel for a notification.
type NotificationChannel string

const (
	NotificationChannelSlack NotificationChannel = "slack"
	NotificationChannelEmail NotificationChannel = "email"
	NotificationChannelLog   NotificationChannel = "log" // always-on fallback
)

// NotificationEvent is the message published to RabbitMQ when an incident
// is created or updated. Notification workers consume this and fan out to
// the configured channels.
type NotificationEvent struct {
	ID         string              `json:"id"`
	IncidentID string              `json:"incident_id"`
	Channel    NotificationChannel `json:"channel"`
	Title      string              `json:"title"`
	Body       string              `json:"body"`
	Severity   LogLevel            `json:"severity"`
	Services   []string            `json:"services"`
	CreatedAt  time.Time           `json:"created_at"`
}
