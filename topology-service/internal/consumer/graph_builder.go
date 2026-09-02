package consumer

import (
	"context"
	"encoding/json"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/pulsetrace/shared/bus"
	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/topology-service/internal/repository"
)

type GraphBuilder struct {
	repo *repository.Repository
}

func NewGraphBuilder(repo *repository.Repository) *GraphBuilder {
	return &GraphBuilder{repo: repo}
}

// Handle is the bus.Handler for the logs topic. The ctx already carries the
// producer's trace context — the bus adapter extracts it, which is why this
// file no longer knows what Kafka is.
func (g *GraphBuilder) Handle(ctx context.Context, msg bus.Message) error {
	tracer := otel.Tracer("topology-service")
	ctx, span := tracer.Start(ctx, "topology.infer_node", trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	var logEntry models.LogEntry
	if err := json.Unmarshal(msg.Value, &logEntry); err != nil {
		log.Printf("topology: failed to unmarshal log entry: %v", err)
		return nil
	}

	tenant := logEntry.TenantID
	if tenant == "" {
		tenant = "default"
	}
	if err := g.repo.UpdateServiceState(ctx, tenant, logEntry.ServiceName, "HEALTHY"); err != nil {
		log.Printf("topology: failed to update state for %s/%s: %v", tenant, logEntry.ServiceName, err)
		span.RecordError(err)
	}
	return nil
}
