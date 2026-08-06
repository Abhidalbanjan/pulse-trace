package models

import "time"

// NotificationChannel identifies the delivery channel for a notification.
type NotificationChannel string

const (
	NotificationChannelSlack     NotificationChannel = "slack"
	NotificationChannelEmail     NotificationChannel = "email"
	NotificationChannelPagerDuty NotificationChannel = "pagerduty"
	NotificationChannelOpsgenie  NotificationChannel = "opsgenie"
	NotificationChannelWebhook   NotificationChannel = "webhook" // generic HMAC-signed HTTP POST
	NotificationChannelLog       NotificationChannel = "log"      // always-on fallback
)

// NotificationAction distinguishes an incident firing from its resolution, so
// on-call channels that support it (PagerDuty, Opsgenie) can auto-close the alert
// they opened rather than leaving it dangling.
type NotificationAction string

const (
	// NotificationActionTriggered is the default (empty string decodes to this):
	// an incident opened or escalated.
	NotificationActionTriggered NotificationAction = "triggered"
	// NotificationActionResolved: the incident has been resolved; channels keyed
	// by incident ID (PagerDuty dedup_key / Opsgenie alias) should close it.
	NotificationActionResolved NotificationAction = "resolved"
)

// NotificationEvent is the message published to RabbitMQ when an incident
// is created, updated, or resolved. Notification workers consume this and fan out
// to the configured channels.
type NotificationEvent struct {
	ID         string              `json:"id"`
	IncidentID string              `json:"incident_id"`
	// TenantID scopes which per-tenant delivery channels (F3) this event routes
	// to. Empty is treated as the "default" tenant for backward compatibility
	// with older publishers and single-tenant deployments.
	TenantID   string              `json:"tenant_id,omitempty"`
	Channel    NotificationChannel `json:"channel"`
	// Action is "triggered" (default) or "resolved". Empty is treated as
	// triggered for backward compatibility with older publishers.
	Action    NotificationAction `json:"action,omitempty"`
	Title     string             `json:"title"`
	Body      string             `json:"body"`
	Severity  LogLevel           `json:"severity"`
	Services  []string           `json:"services"`
	CreatedAt time.Time          `json:"created_at"`
}

// IsResolved reports whether this event marks an incident resolution.
func (e NotificationEvent) IsResolved() bool {
	return e.Action == NotificationActionResolved
}
