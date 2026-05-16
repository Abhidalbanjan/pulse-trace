package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/IBM/sarama"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/rabbitmq"
	"github.com/pulsetrace/shared/telemetry"
)

const (
	serviceName = "correlation-service"
	// correlationWindow is the time window within which alerts from related
	// services are grouped into the same incident.
	correlationWindow = 5 * time.Minute
)

// serviceDependencies maps a service to the set of services it depends on.
// Alerts from dependent services within the correlation window are grouped
// into the same incident, enabling root-cause analysis.
var serviceDependencies = map[string][]string{
	"payment-service": {"postgres", "kafka", "auth-service"},
	"auth-service":    {"postgres", "redis"},
	"order-service":   {"payment-service", "postgres", "kafka"},
	"worker-service":  {"kafka", "postgres"},
	"gateway-service": {"log-service", "alert-service"},
	"log-service":     {"postgres", "kafka"},
	"alert-service":   {"postgres", "kafka"},
}

// rootCauseHints maps common error patterns to probable root causes.
var rootCauseHints = map[string]string{
	"connection":  "Database or network connectivity issue",
	"timeout":     "Downstream service latency or resource exhaustion",
	"memory":      "Memory pressure — possible OOM condition",
	"kafka":       "Kafka broker unavailability or consumer lag",
	"auth":        "Authentication service degradation",
	"permission":  "Authorization failure or misconfigured credentials",
	"crash":       "Application panic or unhandled exception",
	"unavailable": "Upstream service is down or unreachable",
}

// Correlator consumes alerts from Kafka, groups them into incidents, and
// publishes notification events to RabbitMQ.
type Correlator struct {
	repo      *repository.IncidentRepository
	publisher *rabbitmq.Publisher
}

func NewCorrelator(repo *repository.IncidentRepository, publisher *rabbitmq.Publisher) *Correlator {
	return &Correlator{repo: repo, publisher: publisher}
}

// Handle is the Kafka MessageHandler for the alerts topic.
func (c *Correlator) Handle(msg *sarama.ConsumerMessage) error {
	ctx := telemetry.ExtractKafkaContext(context.Background(), msg)

	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(ctx, "correlation.process_alert",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", msg.Topic),
		),
	)
	defer span.End()

	var alert models.Alert
	if err := json.Unmarshal(msg.Value, &alert); err != nil {
		log.Printf("correlator: failed to unmarshal alert at offset %d: %v", msg.Offset, err)
		span.RecordError(err)
		return nil
	}

	span.SetAttributes(
		attribute.String("alert.id", alert.ID),
		attribute.String("alert.service", alert.ServiceName),
		attribute.String("alert.level", string(alert.Level)),
	)

	incident, err := c.correlate(ctx, &alert)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "correlation failed")
		log.Printf("correlator: failed to correlate alert %s: %v", alert.ID, err)
		return err
	}

	span.SetAttributes(attribute.String("incident.id", incident.ID))
	log.Printf("correlator: alert %s → incident %s (%s) alert_count=%d",
		alert.ID, incident.ID, incident.Title, incident.AlertCount)

	// Publish notification event to RabbitMQ.
	if c.publisher != nil {
		if err := c.notify(ctx, incident, &alert); err != nil {
			log.Printf("correlator: failed to publish notification for incident %s: %v", incident.ID, err)
			// Non-fatal — don't fail the Kafka message.
		}
	}

	return nil
}

// correlate finds or creates an incident for the given alert.
func (c *Correlator) correlate(ctx context.Context, alert *models.Alert) (*models.Incident, error) {
	windowStart := alert.TriggeredAt.Add(-correlationWindow)

	// Look for an open incident in the correlation window for this service
	// or any of its dependencies.
	existing, err := c.repo.GetOpenByWindow(ctx, alert.ServiceName, windowStart)
	if err != nil {
		return nil, fmt.Errorf("lookup open incident: %w", err)
	}

	var incident *models.Incident
	if existing != nil {
		// Add this alert to the existing incident.
		incident = existing
	} else {
		// Create a new incident.
		incident = &models.Incident{
			ID:         uuid.New().String(),
			Title:      buildTitle(alert),
			RootCause:  inferRootCause(alert),
			Status:     models.IncidentStatusOpen,
			Severity:   alert.Level,
			AlertCount: 0,
			StartedAt:  alert.TriggeredAt,
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
	}

	return c.repo.Upsert(ctx, incident, alert)
}

// notify publishes a NotificationEvent to RabbitMQ.
func (c *Correlator) notify(ctx context.Context, incident *models.Incident, alert *models.Alert) error {
	event := models.NotificationEvent{
		ID:         uuid.New().String(),
		IncidentID: incident.ID,
		Channel:    models.NotificationChannelLog,
		Title:      incident.Title,
		Body:       fmt.Sprintf("[%s] %s — %s (alert_count=%d)", alert.Level, alert.ServiceName, alert.Message, incident.AlertCount),
		Severity:   incident.Severity,
		Services:   incident.ServiceNames,
		CreatedAt:  time.Now().UTC(),
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	return c.publisher.Publish(ctx, "incident.notification", payload)
}

// buildTitle generates a human-readable incident title from an alert.
func buildTitle(alert *models.Alert) string {
	return fmt.Sprintf("[%s] %s degradation detected", alert.Level, alert.ServiceName)
}

// inferRootCause scans the alert message for known error patterns.
func inferRootCause(alert *models.Alert) string {
	msg := alert.Message
	for keyword, cause := range rootCauseHints {
		if containsIgnoreCase(msg, keyword) {
			return cause
		}
	}
	return fmt.Sprintf("Elevated %s events in %s", alert.Level, alert.ServiceName)
}

func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) &&
		func() bool {
			sl, subl := []rune(s), []rune(substr)
			for i := 0; i <= len(sl)-len(subl); i++ {
				match := true
				for j, r := range subl {
					sr := sl[i+j]
					if sr >= 'A' && sr <= 'Z' {
						sr += 32
					}
					if r >= 'A' && r <= 'Z' {
						r += 32
					}
					if sr != r {
						match = false
						break
					}
				}
				if match {
					return true
				}
			}
			return false
		}()
}
