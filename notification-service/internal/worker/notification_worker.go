package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
type NotificationWorker struct {
	slackWebhook string // optional — set via SLACK_WEBHOOK_URL env var
	smtpHost     string // optional — set via SMTP_HOST env var
}

func NewNotificationWorker() *NotificationWorker {
	return &NotificationWorker{
		slackWebhook: os.Getenv("SLACK_WEBHOOK_URL"),
		smtpHost:     os.Getenv("SMTP_HOST"),
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

// sendSlack posts a notification to a Slack incoming webhook.
// Wire a real SLACK_WEBHOOK_URL to activate this.
func (w *NotificationWorker) sendSlack(_ context.Context, event *models.NotificationEvent) error {
	// Stub: in production, POST to w.slackWebhook with a JSON payload.
	// Using net/http here would add a real HTTP call.
	log.Printf("notification_worker: [SLACK STUB] would post to webhook: %s", event.Title)
	return nil
}

// sendEmail sends a notification via SMTP.
// Wire SMTP_HOST, SMTP_PORT, SMTP_FROM, SMTP_TO to activate this.
func (w *NotificationWorker) sendEmail(_ context.Context, event *models.NotificationEvent) error {
	// Stub: in production, use net/smtp or a library like gomail.
	log.Printf("notification_worker: [EMAIL STUB] would send to %s: %s", w.smtpHost, event.Title)
	return nil
}
