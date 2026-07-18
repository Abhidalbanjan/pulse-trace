package worker

import (
	"bytes"
	"context"
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
	httpClient   *http.Client
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

	return &NotificationWorker{
		slackWebhook: os.Getenv("SLACK_WEBHOOK_URL"),
		smtpHost:     os.Getenv("SMTP_HOST"),
		smtpPort:     smtpPort,
		smtpUsername: os.Getenv("SMTP_USERNAME"),
		smtpPassword: os.Getenv("SMTP_PASSWORD"),
		smtpFrom:     smtpFrom,
		smtpTo:       smtpTo,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
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
