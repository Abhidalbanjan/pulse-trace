package consumer

import (
	"context"
	"encoding/json"
	"log"

	"github.com/IBM/sarama"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/telemetry"
	"github.com/pulsetrace/topology-service/internal/repository"
)

type GraphBuilder struct {
	repo *repository.Neo4jRepository
}

func NewGraphBuilder(repo *repository.Neo4jRepository) *GraphBuilder {
	return &GraphBuilder{repo: repo}
}

func (g *GraphBuilder) Handle(msg *sarama.ConsumerMessage) error {
	ctx := context.Background()
	ctx = telemetry.ExtractKafkaContext(ctx, msg)
	tracer := otel.Tracer("topology-service")
	ctx, span := tracer.Start(ctx, "topology.infer_node", trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	var logEntry models.LogEntry
	if err := json.Unmarshal(msg.Value, &logEntry); err != nil {
		log.Printf("topology: failed to unmarshal log entry: %v", err)
		return nil
	}

	if err := g.repo.UpdateServiceState(ctx, logEntry.ServiceName, "HEALTHY"); err != nil {
		log.Printf("topology: failed to update state for %s: %v", logEntry.ServiceName, err)
		span.RecordError(err)
	}
	return nil
}


