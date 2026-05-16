package consumer

import (
	"context"
	"encoding/json"
	"log"

	"github.com/IBM/sarama"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/pulsetrace/alert-service/internal/repository"
	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/telemetry"
)

const serviceName = "alert-service"

// alertLevels defines which log levels trigger an alert.
var alertLevels = map[models.LogLevel]bool{
	models.LogLevelError: true,
	models.LogLevelFatal: true,
}

// LogConsumer processes log events from Kafka and creates alerts for
// ERROR and FATAL entries.
type LogConsumer struct {
	repo *repository.AlertRepository
}

func NewLogConsumer(repo *repository.AlertRepository) *LogConsumer {
	return &LogConsumer{repo: repo}
}

// Handle is the MessageHandler passed to kafka.ConsumerGroup.
// It extracts the upstream trace context from Kafka headers so the alert
// creation span is a child of the log-service publish span.
func (c *LogConsumer) Handle(msg *sarama.ConsumerMessage) error {
	// Extract upstream W3C trace context from Kafka message headers.
	ctx := telemetry.ExtractKafkaContext(context.Background(), msg)

	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(ctx, "alert.process_log_event",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", msg.Topic),
			attribute.Int64("messaging.kafka.partition", int64(msg.Partition)),
			attribute.Int64("messaging.kafka.offset", msg.Offset),
		),
	)
	defer span.End()

	var entry models.LogEntry
	if err := json.Unmarshal(msg.Value, &entry); err != nil {
		log.Printf("log_consumer: failed to unmarshal message at offset %d: %v", msg.Offset, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "unmarshal failed")
		return nil // don't retry malformed messages
	}

	span.SetAttributes(
		attribute.String("log.id", entry.ID),
		attribute.String("log.service", entry.ServiceName),
		attribute.String("log.level", string(entry.Level)),
	)

	if !alertLevels[entry.Level] {
		return nil // not an alertable level
	}

	_, dbSpan := tracer.Start(ctx, "db.insert_alert")
	alert, err := c.repo.Insert(ctx, &entry)
	dbSpan.End()
	if err != nil {
		log.Printf("log_consumer: failed to store alert for log %s: %v", entry.ID, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "db insert failed")
		return err
	}

	span.SetAttributes(attribute.String("alert.id", alert.ID))
	log.Printf("log_consumer: alert created id=%s service=%s level=%s trace_id=%s",
		alert.ID, alert.ServiceName, alert.Level, alert.TraceID)
	return nil
}
