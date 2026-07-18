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
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/telemetry"
)

const (
	serviceName = "alert-service"
	alertsTopic = "alerts"
)

// alertLevels defines which log levels trigger an alert.
var alertLevels = map[models.LogLevel]bool{
	models.LogLevelError: true,
	models.LogLevelFatal: true,
}

// LogConsumer processes log events from Kafka and creates alerts for
// ERROR and FATAL entries, then publishes them to the alerts topic for
// the correlation engine to consume.
type LogConsumer struct {
	repo     *repository.AlertRepository
	producer *kafka.Producer // may be nil — degrades gracefully
}

func NewLogConsumer(repo *repository.AlertRepository, producer *kafka.Producer) *LogConsumer {
	return &LogConsumer{repo: repo, producer: producer}
}

// Handle is the MessageHandler passed to kafka.ConsumerGroup.
func (c *LogConsumer) Handle(msg *sarama.ConsumerMessage) error {
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
		return nil
	}

	span.SetAttributes(
		attribute.String("log.id", entry.ID),
		attribute.String("log.service", entry.ServiceName),
		attribute.String("log.level", string(entry.Level)),
	)

	if !alertLevels[entry.Level] {
		return nil
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

	// Retention filter: a FATAL alert is a critical event worth keeping regardless
	// of sampling/cost policy - force it past the collector's tail_sampling.
	if entry.Level == models.LogLevelFatal {
		telemetry.ForceKeep(ctx)
	}

	// Publish alert to the alerts topic so the correlation engine can group it.
	if c.producer != nil {
		go func() {
			_, kafkaSpan := tracer.Start(ctx, "kafka.publish_alert")
			defer kafkaSpan.End()

			payload, err := json.Marshal(alert)
			if err != nil {
				kafkaSpan.RecordError(err)
				log.Printf("log_consumer: failed to marshal alert for kafka: %v", err)
				return
			}
			if err := c.producer.PublishWithContext(ctx, alertsTopic, alert.ServiceName, payload); err != nil {
				kafkaSpan.RecordError(err)
				log.Printf("log_consumer: failed to publish alert %s to kafka: %v", alert.ID, err)
			}
		}()
	}

	return nil
}
