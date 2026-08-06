package channels

import "time"

// Channel types.
const (
	TypeSlack     = "slack"
	TypeEmail     = "email"
	TypePagerDuty = "pagerduty"
	TypeOpsgenie  = "opsgenie"
	TypeWebhook   = "webhook"
)

// secretKeys lists, per channel type, which config keys hold secrets and must be
// encrypted at rest / redacted in API responses.
var secretKeys = map[string]map[string]bool{
	TypeSlack:     {"webhook_url": true},
	TypeEmail:     {"password": true},
	TypePagerDuty: {"routing_key": true},
	TypeOpsgenie:  {"api_key": true},
	TypeWebhook:   {"url": true, "secret": true},
}

// requiredKeys lists the minimum config a channel type needs to be usable.
var requiredKeys = map[string][]string{
	TypeSlack:     {"webhook_url"},
	TypeEmail:     {"host", "to"},
	TypePagerDuty: {"routing_key"},
	TypeOpsgenie:  {"api_key"},
	TypeWebhook:   {"url"},
}

// ValidType reports whether t is a known channel type.
func ValidType(t string) bool {
	_, ok := secretKeys[t]
	return ok
}

// IsSecret reports whether a config key for a channel type holds a secret.
func IsSecret(channelType, key string) bool {
	return secretKeys[channelType][key]
}

// Channel is a tenant's configured delivery destination. Config holds the
// type-specific fields; secret values are stored encrypted and never leave the
// API in the clear.
type Channel struct {
	ID        string            `json:"id"`
	TenantID  string            `json:"tenant_id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Config    map[string]string `json:"config"`
	Enabled   bool              `json:"enabled"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// MissingRequired returns the required config keys absent from cfg, so create
// fails with a precise message instead of delivering to a half-configured channel.
func MissingRequired(channelType string, cfg map[string]string) []string {
	var missing []string
	for _, k := range requiredKeys[channelType] {
		if v, ok := cfg[k]; !ok || v == "" {
			missing = append(missing, k)
		}
	}
	return missing
}

// Redacted returns a copy of the channel safe to send to the UI: secret config
// values are replaced with a "" and a companion boolean (via HasSecrets) tells
// the UI a value is set without revealing it.
func (c Channel) Redacted() Channel {
	out := c
	out.Config = map[string]string{}
	for k, v := range c.Config {
		if IsSecret(c.Type, k) {
			if v != "" {
				out.Config[k+"_set"] = "true" // presence flag, never the value
			}
			continue
		}
		out.Config[k] = v
	}
	return out
}
