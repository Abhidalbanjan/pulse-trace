package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"

	"github.com/pulsetrace/shared/models"
)

const (
	pagerdutyEventsURL = "https://events.pagerduty.com/v2/enqueue"
	opsgenieDefaultURL = "https://api.opsgenie.com/v2/alerts"
)

// Deliver dispatches a notification event to a single channel using its
// (decrypted) config. It is the one delivery path shared by test-send and the
// worker's per-tenant DB channels, so behavior can't drift between "test" and
// "real". A disabled channel is a no-op.
func Deliver(ctx context.Context, client *http.Client, ch Channel, event *models.NotificationEvent) error {
	if !ch.Enabled {
		return nil
	}
	switch ch.Type {
	case TypeSlack:
		return deliverSlack(ctx, client, ch.Config, event)
	case TypeEmail:
		return deliverEmail(ch.Config, event)
	case TypePagerDuty:
		return deliverPagerDuty(ctx, client, ch.Config, event)
	case TypeOpsgenie:
		return deliverOpsgenie(ctx, client, ch.Config, event)
	case TypeWebhook:
		return deliverWebhook(ctx, client, ch.Config, event)
	default:
		return fmt.Errorf("unknown channel type %q", ch.Type)
	}
}

func deliverSlack(ctx context.Context, client *http.Client, cfg map[string]string, event *models.NotificationEvent) error {
	webhook := cfg["webhook_url"]
	if webhook == "" {
		return fmt.Errorf("slack channel missing webhook_url")
	}
	icon, label := ":rotating_light:", strings.ToUpper(string(event.Severity))
	if event.IsResolved() {
		icon, label = ":white_check_mark:", "RESOLVED"
	}
	text := fmt.Sprintf("%s *[%s]* %s\n%s\n_Services: %s · Incident: %s_",
		icon, label, event.Title, event.Body, strings.Join(event.Services, ", "), event.IncidentID)
	body, _ := json.Marshal(map[string]string{"text": text})
	return postJSON(ctx, client, http.MethodPost, webhook, body, nil, http.StatusOK, http.StatusNoContent)
}

func deliverEmail(cfg map[string]string, event *models.NotificationEvent) error {
	host := cfg["host"]
	to := splitList(cfg["to"])
	if host == "" || len(to) == 0 {
		return fmt.Errorf("email channel missing host or to")
	}
	port := cfg["port"]
	if port == "" {
		port = "587"
	}
	from := cfg["from"]
	if from == "" {
		from = "alerts@pulsetrace.local"
	}
	tag := strings.ToUpper(string(event.Severity))
	if event.IsResolved() {
		tag = "RESOLVED"
	}
	subject := fmt.Sprintf("[PulseTrace][%s] %s", tag, event.Title)
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\n\nServices: %s\nIncident: %s\n",
		from, strings.Join(to, ", "), subject, event.Body, strings.Join(event.Services, ", "), event.IncidentID)

	var auth smtp.Auth
	if u := cfg["username"]; u != "" {
		auth = smtp.PlainAuth("", u, cfg["password"], host)
	}
	if err := smtp.SendMail(host+":"+port, auth, from, to, []byte(msg)); err != nil {
		return fmt.Errorf("send mail via %s:%s: %w", host, port, err)
	}
	return nil
}

func deliverPagerDuty(ctx context.Context, client *http.Client, cfg map[string]string, event *models.NotificationEvent) error {
	routingKey := cfg["routing_key"]
	if routingKey == "" {
		return fmt.Errorf("pagerduty channel missing routing_key")
	}
	source := "pulsetrace"
	if len(event.Services) > 0 {
		source = event.Services[0]
	}
	payload := map[string]any{
		"routing_key":  routingKey,
		"event_action": "trigger",
		"dedup_key":    event.IncidentID,
		"payload": map[string]any{
			"summary":  fmt.Sprintf("[%s] %s", strings.ToUpper(string(event.Severity)), event.Title),
			"source":   source,
			"severity": pagerdutySeverity(event.Severity),
			"custom_details": map[string]string{
				"incident_id": event.IncidentID,
				"services":    strings.Join(event.Services, ", "),
				"body":        event.Body,
			},
		},
	}
	if event.IsResolved() {
		payload["event_action"] = "resolve"
		delete(payload, "payload")
	}
	body, _ := json.Marshal(payload)
	endpoint := cfg["url"]
	if endpoint == "" {
		endpoint = pagerdutyEventsURL
	}
	return postJSON(ctx, client, http.MethodPost, endpoint, body, nil, http.StatusAccepted)
}

func deliverOpsgenie(ctx context.Context, client *http.Client, cfg map[string]string, event *models.NotificationEvent) error {
	apiKey := cfg["api_key"]
	if apiKey == "" {
		return fmt.Errorf("opsgenie channel missing api_key")
	}
	base := cfg["api_url"]
	if base == "" {
		base = opsgenieDefaultURL
	}
	headers := map[string]string{"Authorization": "GenieKey " + apiKey}

	if event.IsResolved() {
		closeURL := fmt.Sprintf("%s/%s/close?identifierType=alias", strings.TrimRight(base, "/"), url.PathEscape(event.IncidentID))
		body, _ := json.Marshal(map[string]string{"source": "pulsetrace", "note": event.Body})
		return postJSON(ctx, client, http.MethodPost, closeURL, body, headers, http.StatusAccepted, http.StatusNotFound)
	}
	alert := map[string]any{
		"message":     truncate(event.Title, 130),
		"alias":       event.IncidentID,
		"description": fmt.Sprintf("%s\n\nServices: %s\nIncident: %s", event.Body, strings.Join(event.Services, ", "), event.IncidentID),
		"priority":    opsgeniePriority(event.Severity),
		"tags":        event.Services,
		"source":      "pulsetrace",
	}
	body, _ := json.Marshal(alert)
	return postJSON(ctx, client, http.MethodPost, base, body, headers, http.StatusAccepted)
}

func deliverWebhook(ctx context.Context, client *http.Client, cfg map[string]string, event *models.NotificationEvent) error {
	endpoint := cfg["url"]
	if endpoint == "" {
		return fmt.Errorf("webhook channel missing url")
	}
	body, _ := json.Marshal(event)
	headers := map[string]string{}
	if secret := cfg["secret"]; secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		headers["X-PulseTrace-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	// Accept any 2xx.
	return postJSON(ctx, client, http.MethodPost, endpoint, body, headers, 200, 201, 202, 204)
}

// postJSON POSTs body and verifies the response status is one of okStatuses.
func postJSON(ctx context.Context, client *http.Client, method, endpoint string, body []byte, headers map[string]string, okStatuses ...int) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for _, s := range okStatuses {
		if resp.StatusCode == s {
			return nil
		}
	}
	return fmt.Errorf("%s returned status %d", endpoint, resp.StatusCode)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func pagerdutySeverity(level models.LogLevel) string {
	switch level {
	case models.LogLevelFatal, models.LogLevelError:
		return "critical"
	case models.LogLevelWarning:
		return "warning"
	default:
		return "error"
	}
}

func opsgeniePriority(level models.LogLevel) string {
	switch level {
	case models.LogLevelFatal:
		return "P1"
	case models.LogLevelError:
		return "P2"
	case models.LogLevelWarning:
		return "P3"
	default:
		return "P4"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
