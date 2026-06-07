package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/causal"
	"github.com/pulsetrace/shared/client"
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

// causalAnalysisTimeout bounds a single async causal-AI call. Claude responses
// arrive in well under 10s in practice; the extra headroom covers network jitter.
const causalAnalysisTimeout = 45 * time.Second

// Correlator consumes alerts from Kafka, groups them into incidents, and
// publishes notification events to RabbitMQ. After every incident upsert, it
// asynchronously runs causal-AI analysis to produce a refined root-cause
// hypothesis and narrative; failures here are non-fatal.
type Correlator struct {
	repo      *repository.IncidentRepository
	publisher *rabbitmq.Publisher
	analyzer  causal.Analyzer
	topoclient *client.TopologyClient
	// inflight dedupes concurrent analyses for the same incident — a burst of
	// alerts hitting the same open incident only triggers one in-flight call.
	inflight sync.Map // map[string]struct{}
}

func NewCorrelator(repo *repository.IncidentRepository, publisher *rabbitmq.Publisher, analyzer causal.Analyzer, topoclient *client.TopologyClient) *Correlator {
	if analyzer == nil {
		analyzer = &causal.NoopAnalyzer{}
	}
	return &Correlator{repo: repo, publisher: publisher, analyzer: analyzer, topoclient: topoclient}
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

	// Predictive Engine Logic: flag downstream services as PREDICTIVE_WARNING
	go func() {
		downstream, err := c.topoclient.GetDownstreamDependencies(context.Background(), alert.ServiceName)
		if err != nil {
			log.Printf("correlator: failed to get downstream for %s: %v", alert.ServiceName, err)
			return
		}
		for _, ds := range downstream {
			log.Printf("PREDICTIVE ENGINE: Marking %s as PREDICTIVE_WARNING due to %s degradation", ds, alert.ServiceName)
			if err := c.topoclient.UpdateServiceState(context.Background(), ds, "PREDICTIVE_WARNING"); err != nil {
				log.Printf("correlator: failed to update predictive state for %s: %v", ds, err)
			}
		}
	}()

	// Kick off causal-AI analysis asynchronously. We don't propagate ctx —
	// the analysis must survive the Kafka message lifecycle.
	c.scheduleCausalAnalysis(incident.ID)

	return nil
}

// scheduleCausalAnalysis runs the configured causal Analyzer in the background
// and persists the result. Concurrent calls for the same incident are deduped
// via the inflight sync.Map — only one analysis runs per incident at a time.
func (c *Correlator) scheduleCausalAnalysis(incidentID string) {
	if _, busy := c.inflight.LoadOrStore(incidentID, struct{}{}); busy {
		return
	}

	go func() {
		defer c.inflight.Delete(incidentID)

		ctx, cancel := context.WithTimeout(context.Background(), causalAnalysisTimeout)
		defer cancel()

		tracer := otel.Tracer(serviceName)
		ctx, span := tracer.Start(ctx, "causal.analyze",
			trace.WithAttributes(
				attribute.String("incident.id", incidentID),
				attribute.String("analyzer", c.analyzer.Name()),
			),
		)
		defer span.End()

		inc, err := c.repo.GetByID(ctx, incidentID)
		if err != nil {
			span.RecordError(err)
			log.Printf("causal: lookup incident %s: %v", incidentID, err)
			return
		}
		alerts, err := c.repo.AlertsForIncident(ctx, incidentID)
		if err != nil {
			span.RecordError(err)
			log.Printf("causal: load alerts for %s: %v", incidentID, err)
			return
		}

		deps := make(map[string][]string)
		for _, svc := range inc.ServiceNames {
			upstream, err := c.topoclient.GetUpstreamDependencies(ctx, svc)
			if err != nil {
				log.Printf("causal: failed to get upstream for %s: %v", svc, err)
				continue
			}
			deps[svc] = upstream
		}

		evidence := buildEvidence(inc, alerts, deps)

		result, err := c.analyzer.Analyze(ctx, evidence)
		if err != nil {
			span.RecordError(err)
			log.Printf("causal: analyzer %s failed for %s: %v — falling back to rule-based",
				c.analyzer.Name(), incidentID, err)
			// Fallback to deterministic analyzer so the incident still gets a chain.
			fallback := &causal.NoopAnalyzer{}
			result, err = fallback.Analyze(ctx, evidence)
			if err != nil {
				log.Printf("causal: fallback also failed for %s: %v", incidentID, err)
				return
			}
		}

		if err := c.repo.UpdateCausalAnalysis(ctx, incidentID, result); err != nil {
			span.RecordError(err)
			log.Printf("causal: persist analysis for %s: %v", incidentID, err)
			return
		}

		// Option 3 Integration: push the causal chain path to Neo4j via topology client!
		log.Printf("causal: pushing causal chain of length %d to Neo4j topology", len(result.Chain))
		if err := c.topoclient.UpdateCausalPath(ctx, result.Chain); err != nil {
			log.Printf("causal: failed to push causal path to topology service: %v", err)
		}

		span.SetAttributes(
			attribute.Float64("causal.confidence", result.Confidence),
			attribute.Int("causal.chain_length", len(result.Chain)),
		)
		log.Printf("causal: incident %s analyzed by %s — confidence=%.2f, chain_links=%d",
			incidentID, result.Model, result.Confidence, len(result.Chain))
	}()
}

// buildEvidence assembles the analyzer input from incident + alerts.
// Kept as a small helper so the trigger goroutine reads top-to-bottom.
func buildEvidence(inc *models.Incident, alerts []models.IncidentAlert, deps map[string][]string) *causal.Evidence {
	return &causal.Evidence{
		Incident:     inc,
		Alerts:       alerts,
		Dependencies: deps,
		Window:       correlationWindow,
	}
}

// correlate finds or creates an incident for the given alert.
func (c *Correlator) correlate(ctx context.Context, alert *models.Alert) (*models.Incident, error) {
	windowStart := alert.TriggeredAt.Add(-correlationWindow)

	// Fetch upstream dependencies to see if this alert cascades from an existing incident.
	upstream, err := c.topoclient.GetUpstreamDependencies(ctx, alert.ServiceName)
	if err != nil {
		log.Printf("correlator: failed to get upstream for %s: %v", alert.ServiceName, err)
	}
	candidateServices := append(upstream, alert.ServiceName)

	// Look for an open incident in the correlation window for this service
	// or any of its dependencies.
	existing, err := c.repo.GetOpenByWindow(ctx, candidateServices, windowStart)
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
