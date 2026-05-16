package telemetry

import (
	"context"

	"github.com/IBM/sarama"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// kafkaHeaderCarrier adapts a Sarama message header slice to the OTel
// TextMapCarrier interface so W3C traceparent/tracestate can be injected
// into Kafka messages and extracted from them.

// ── Inject (producer side) ────────────────────────────────────────────────────

// kafkaInjectCarrier wraps a *[]sarama.RecordHeader for injection.
type kafkaInjectCarrier struct {
	headers *[]sarama.RecordHeader
}

func (c kafkaInjectCarrier) Set(key, value string) {
	*c.headers = append(*c.headers, sarama.RecordHeader{
		Key:   []byte(key),
		Value: []byte(value),
	})
}

func (c kafkaInjectCarrier) Get(key string) string { return "" }
func (c kafkaInjectCarrier) Keys() []string        { return nil }

// InjectKafkaHeaders injects the W3C trace context from ctx into the
// ProducerMessage headers so downstream consumers can continue the trace.
func InjectKafkaHeaders(ctx context.Context, msg *sarama.ProducerMessage) {
	carrier := kafkaInjectCarrier{headers: &msg.Headers}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
}

// ── Extract (consumer side) ───────────────────────────────────────────────────

// ExtractKafkaContext extracts the W3C trace context from a ConsumerMessage's
// headers and returns a child context that continues the upstream trace.
func ExtractKafkaContext(ctx context.Context, msg *sarama.ConsumerMessage) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(headerSliceToMap(msg.Headers)))
}

// headerSliceToMap converts Sarama record headers to a plain string map for
// use with propagation.MapCarrier.
func headerSliceToMap(headers []*sarama.RecordHeader) map[string]string {
	m := make(map[string]string, len(headers))
	for _, h := range headers {
		if h != nil {
			m[string(h.Key)] = string(h.Value)
		}
	}
	return m
}
