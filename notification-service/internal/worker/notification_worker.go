package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/pulsetrace/shared/models"
)

const serviceName = "notification-service"

// NotificationWorker processes notification events from RabbitMQ and
// dispatches them to the configured channels (Slack, email, log).
//
// Slack and email were previously log.Printf stubs that never made a real
// network call — an incident could fire and nobody would actually be paged.
// Both channels now make real outbound calls when configured; if a channel's
// env vars aren't set, that channel is simply skipped (the always-on log
// channel still fires), rather than pretending to have delivered anything.
type NotificationWorker struct {
	slackWebhook string // optional — set via SLACK_WEBHOOK_URL env var
	smtpHost     string // optional — set via SMTP_HOST env var
	smtpPort     string
	smtpUsername string
	smtpPassword string
	smtpFrom     string
	smtpTo       []string

	// PagerDuty Events API v2 — set via PAGERDUTY_ROUTING_KEY (the integration
	// key of a service's Events API v2 integration). pagerdutyURL is the fixed
	// PagerDuty endpoint, a field only so tests can point it at a stub.
	pagerdutyRoutingKey string
	pagerdutyURL        string

	// Opsgenie Alerts API — set via OPSGENIE_API_KEY. OPSGENIE_API_URL overrides
	// the default US endpoint (EU customers point it at api.eu.opsgenie.com).
	opsgenieAPIKey string
	opsgenieAPIURL string

	// Generic webhook — set via WEBHOOK_URL. When WEBHOOK_SECRET is also set,
	// every request is signed so the receiver can verify it came from us (same
	// HMAC-SHA256 scheme the rest of the platform uses for signed callbacks).
	webhookURL    string
	webhookSecret string

	httpClient *http.Client
}

func NewNotificationWorker() *NotificationWorker {
	smtpTo := []string{}
	if raw := os.Getenv("SMTP_TO"); raw != "" {
		for _, addr := range strings.Split(raw, ",") {
			if addr = strings.TrimSpace(addr); addr != "" {
				smtpTo = append(smtpTo, addr)
			}
		}
	}

	smtpPort := os.Getenv("SMTP_PORT")
	if smtpPort == "" {
		smtpPort = "587"
	}

	smtpFrom := os.Getenv("SMTP_FROM")
	if smtpFrom == "" {
		smtpFrom = "alerts@pulsetrace.local"
	}

	opsgenieAPIURL := os.Getenv("OPSGENIE_API_URL")
	if opsgenieAPIURL == "" {
		opsgenieAPIURL = "https://api.opsgenie.com/v2/alerts"
	}

	return &NotificationWorker{
		slackWebhook:        os.Getenv("SLACK_WEBHOOK_URL"),
		smtpHost:            os.Getenv("SMTP_HOST"),
		smtpPort:            smtpPort,
		smtpUsername:        os.Getenv("SMTP_USERNAME"),
		smtpPassword:        os.Getenv("SMTP_PASSWORD"),
		smtpFrom:            smtpFrom,
		smtpTo:              smtpTo,
		pagerdutyRoutingKey: os.Getenv("PAGERDUTY_ROUTING_KEY"),
		pagerdutyURL:        pagerdutyEventsURL,
		opsgenieAPIKey:      os.Getenv("OPSGENIE_API_KEY"),
		opsgenieAPIURL:      opsgenieAPIURL,
		webhookURL:          os.Getenv("WEBHOOK_URL"),
		webhookSecret:       os.Getenv("WEBHOOK_SECRET"),
		httpClient:          &http.Client{Timeout: 10 * time.Second},
	}
}

// Handle processes a single notification event from RabbitMQ.
func (w *NotificationWorker) Handle(ctx context.Context, body []byte) error {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(ctx, "notification.dispatch")
	defer span.End()

	var event models.NotificationEvent
	if err := json.Unmarshal(body, &event); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "unmarshal failed")
		log.Printf("notification_worker: failed to unmarshal event: %v", err)
		return nil // don't retry malformed messages
	}

	span.SetAttributes(
		attribute.String("notification.id", event.ID),
		attribute.String("notification.incident_id", event.IncidentID),
		attribute.String("notification.severity", string(event.Severity)),
		attribute.String("notification.channel", string(event.Channel)),
	)

	log.Printf("notification_worker: dispatching incident=%s severity=%s title=%q services=%s",
		event.IncidentID, event.Severity, event.Title, strings.Join(event.Services, ","))

	var errs []string

	// Always log — this is the always-on fallback channel.
	w.logNotification(&event)

	// Slack (if webhook configured).
	if w.slackWebhook != "" {
		if err := w.sendSlack(ctx, &event); err != nil {
			errs = append(errs, fmt.Sprintf("slack: %v", err))
		}
	}

	// Email (if SMTP configured).
	if w.smtpHost != "" {
		if err := w.sendEmail(ctx, &event); err != nil {
			errs = append(errs, fmt.Sprintf("email: %v", err))
		}
	}

	// PagerDuty (if a routing key is configured).
	if w.pagerdutyRoutingKey != "" {
		if err := w.sendPagerDuty(ctx, &event); err != nil {
			errs = append(errs, fmt.Sprintf("pagerduty: %v", err))
		}
	}

	// Opsgenie (if an API key is configured).
	if w.opsgenieAPIKey != "" {
		if err := w.sendOpsgenie(ctx, &event); err != nil {
			errs = append(errs, fmt.Sprintf("opsgenie: %v", err))
		}
	}

	// Generic webhook (if a URL is configured).
	if w.webhookURL != "" {
		if err := w.sendWebhook(ctx, &event); err != nil {
			errs = append(errs, fmt.Sprintf("webhook: %v", err))
		}
	}

	if len(errs) > 0 {
		err := fmt.Errorf("partial dispatch failures: %s", strings.Join(errs, "; "))
		span.RecordError(err)
		return err
	}

	return nil
}

// logNotification writes a structured notification to stdout.
// This is always active and serves as the audit trail.
func (w *NotificationWorker) logNotification(event *models.NotificationEvent) {
	log.Printf("🚨 INCIDENT NOTIFICATION\n"+
		"  ID:        %s\n"+
		"  Incident:  %s\n"+
		"  Title:     %s\n"+
		"  Severity:  %s\n"+
		"  Services:  %s\n"+
		"  Body:      %s\n"+
		"  Time:      %s",
		event.ID,
		event.IncidentID,
		event.Title,
		event.Severity,
		strings.Join(event.Services, ", "),
		event.Body,
		event.CreatedAt.Format(time.RFC3339),
	)
}

// slackPayload is a minimal Slack incoming-webhook message. Slack renders
// "text" as the notification body; we lead with severity + title so it's
// scannable in a channel without opening the message.
type slackPayload struct {
	Text string `json:"text"`
}

// sendSlack posts a real notification to a Slack incoming webhook.
// Activated by setting SLACK_WEBHOOK_URL.
func (w *NotificationWorker) sendSlack(ctx context.Context, event *models.NotificationEvent) error {
	text := fmt.Sprintf(":rotating_light: *[%s] %s*\n%s\nServices: %s\nIncident: %s",
		strings.ToUpper(string(event.Severity)), event.Title, event.Body,
		strings.Join(event.Services, ", "), event.IncidentID)

	body, err := json.Marshal(slackPayload{Text: text})
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.slackWebhook, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post to slack: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}

	log.Printf("notification_worker: delivered to Slack (incident=%s)", event.IncidentID)
	return nil
}

// sendEmail sends a real notification via SMTP with STARTTLS/plain auth.
// Activated by setting SMTP_HOST (and SMTP_TO for at least one recipient).
func (w *NotificationWorker) sendEmail(_ context.Context, event *models.NotificationEvent) error {
	if len(w.smtpTo) == 0 {
		return fmt.Errorf("SMTP_HOST is set but SMTP_TO has no recipients configured")
	}

	subject := fmt.Sprintf("[PulseTrace][%s] %s", strings.ToUpper(string(event.Severity)), event.Title)
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"+
			"%s\n\nServices: %s\nIncident: %s\nTime: %s\n",
		w.smtpFrom, strings.Join(w.smtpTo, ", "), subject,
		event.Body, strings.Join(event.Services, ", "), event.IncidentID, event.CreatedAt.Format(time.RFC3339),
	)

	addr := fmt.Sprintf("%s:%s", w.smtpHost, w.smtpPort)

	var auth smtp.Auth
	if w.smtpUsername != "" {
		auth = smtp.PlainAuth("", w.smtpUsername, w.smtpPassword, w.smtpHost)
	}

	if err := smtp.SendMail(addr, auth, w.smtpFrom, w.smtpTo, []byte(msg)); err != nil {
		return fmt.Errorf("send mail via %s: %w", addr, err)
	}

	log.Printf("notification_worker: delivered email to %s (incident=%s)", strings.Join(w.smtpTo, ", "), event.IncidentID)
	return nil
}

const pagerdutyEventsURL = "https://events.pagerduty.com/v2/enqueue"

// pagerdutyEvent is a PagerDuty Events API v2 "trigger" payload. DedupKey is the
// incident ID so repeated notifications for the same incident coalesce into one
// PagerDuty alert (and auto-resolve semantics stay possible later) rather than
// paging on-call once per notification.
type pagerdutyEvent struct {
	RoutingKey  string           `json:"routing_key"`
	EventAction string           `json:"event_action"`
	DedupKey    string           `json:"dedup_key,omitempty"`
	Payload     pagerdutyPayload `json:"payload"`
}

type pagerdutyPayload struct {
	Summary       string            `json:"summary"`
	Source        string            `json:"source"`
	Severity      string            `json:"severity"`
	CustomDetails map[string]string `json:"custom_details,omitempty"`
}

// sendPagerDuty triggers a PagerDuty Events API v2 alert. Activated by
// PAGERDUTY_ROUTING_KEY.
func (w *NotificationWorker) sendPagerDuty(ctx context.Context, event *models.NotificationEvent) error {
	source := "pulsetrace"
	if len(event.Services) > 0 {
		source = event.Services[0]
	}

	payload := pagerdutyEvent{
		RoutingKey:  w.pagerdutyRoutingKey,
		EventAction: "trigger",
		DedupKey:    event.IncidentID,
		Payload: pagerdutyPayload{
			// PagerDuty caps the summary at 1024 chars; the title alone is well
			// within that and is what shows in the alert list.
			Summary:  fmt.Sprintf("[%s] %s", strings.ToUpper(string(event.Severity)), event.Title),
			Source:   source,
			Severity: pagerdutySeverity(event.Severity),
			CustomDetails: map[string]string{
				"incident_id": event.IncidentID,
				"services":    strings.Join(event.Services, ", "),
				"body":        event.Body,
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal pagerduty payload: %w", err)
	}

	url := w.pagerdutyURL
	if url == "" {
		url = pagerdutyEventsURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build pagerduty request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post to pagerduty: %w", err)
	}
	defer resp.Body.Close()

	// Events API v2 returns 202 Accepted on success.
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("pagerduty returned status %d", resp.StatusCode)
	}

	log.Printf("notification_worker: delivered to PagerDuty (incident=%s)", event.IncidentID)
	return nil
}

// opsgenieAlert is an Opsgenie Alerts API create-alert payload. Alias is the
// incident ID so Opsgenie de-duplicates repeated notifications for the same
// incident into a single open alert.
type opsgenieAlert struct {
	Message     string   `json:"message"`
	Alias       string   `json:"alias,omitempty"`
	Description string   `json:"description,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Source      string   `json:"source,omitempty"`
}

// sendOpsgenie creates an Opsgenie alert. Activated by OPSGENIE_API_KEY.
func (w *NotificationWorker) sendOpsgenie(ctx context.Context, event *models.NotificationEvent) error {
	alert := opsgenieAlert{
		// Opsgenie caps message at 130 chars; keep it to the title and let the
		// full context ride in the description.
		Message:     truncate(event.Title, 130),
		Alias:       event.IncidentID,
		Description: fmt.Sprintf("%s\n\nServices: %s\nIncident: %s", event.Body, strings.Join(event.Services, ", "), event.IncidentID),
		Priority:    opsgeniePriority(event.Severity),
		Tags:        event.Services,
		Source:      "pulsetrace",
	}

	body, err := json.Marshal(alert)
	if err != nil {
		return fmt.Errorf("marshal opsgenie payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.opsgenieAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build opsgenie request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Opsgenie authenticates with a GenieKey-scheme Authorization header.
	req.Header.Set("Authorization", "GenieKey "+w.opsgenieAPIKey)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post to opsgenie: %w", err)
	}
	defer resp.Body.Close()

	// Opsgenie returns 202 Accepted (the create is processed asynchronously).
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("opsgenie returned status %d", resp.StatusCode)
	}

	log.Printf("notification_worker: delivered to Opsgenie (incident=%s)", event.IncidentID)
	return nil
}

// sendWebhook POSTs the raw notification event to a generic endpoint. When
// WEBHOOK_SECRET is set, an X-PulseTrace-Signature header carries an HMAC-SHA256
// of the exact request body so the receiver can verify the call is genuinely
// from us and hasn't been tampered with — the same scheme used elsewhere in the
// platform for signed callbacks.
func (w *NotificationWorker) sendWebhook(ctx context.Context, event *models.NotificationEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if w.webhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(w.webhookSecret))
		mac.Write(body)
		req.Header.Set("X-PulseTrace-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post to webhook: %w", err)
	}
	defer resp.Body.Close()

	// Accept any 2xx — a generic receiver may legitimately answer 200/201/202/204.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	log.Printf("notification_worker: delivered to webhook (incident=%s)", event.IncidentID)
	return nil
}

// pagerdutySeverity maps our LogLevel severities onto PagerDuty's fixed
// severity enum (critical/error/warning/info). Anything unrecognized falls back
// to "error" so an unmapped level still pages rather than being silently downgraded.
func pagerdutySeverity(level models.LogLevel) string {
	switch level {
	case models.LogLevelFatal:
		return "critical"
	case models.LogLevelError:
		return "error"
	case models.LogLevelWarning:
		return "warning"
	case models.LogLevelInfo, models.LogLevelDebug:
		return "info"
	default:
		return "error"
	}
}

// opsgeniePriority maps our LogLevel severities onto Opsgenie's P1–P5 scale.
func opsgeniePriority(level models.LogLevel) string {
	switch level {
	case models.LogLevelFatal:
		return "P1"
	case models.LogLevelError:
		return "P2"
	case models.LogLevelWarning:
		return "P3"
	case models.LogLevelInfo:
		return "P4"
	case models.LogLevelDebug:
		return "P5"
	default:
		return "P2"
	}
}

// truncate shortens s to at most n bytes, so a channel's field-length cap can't
// bounce an otherwise-valid alert.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
