package bus

// Trace-context propagation, at the port rather than in an adapter.
//
// # Why it lives here
//
// Every consumer used to open with `telemetry.ExtractKafkaContext(ctx, msg)`,
// which takes a `*sarama.ConsumerMessage`. That is what made the trace boundary
// a Kafka-shaped problem: an in-process transport carrying the same headers
// could not reuse a line of it, so P1.2 would have had to write its own — and a
// second implementation of "continue the producer's trace" is a second chance
// to get it subtly wrong and silently start a new trace instead.
//
// Headers are a `map[string][]byte` on Message, which every transport can
// produce, so extraction belongs at that level. Both adapters inherit it.

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// traceCarrier is a propagation carrier over a plain string map.
type traceCarrier = propagation.MapCarrier

// injectTrace writes ctx's outgoing trace context into the carrier, so a
// transport with no client library of its own still propagates identically to
// the Kafka one.
func injectTrace(ctx context.Context, carrier traceCarrier) {
	otel.GetTextMapPropagator().Inject(ctx, carrier)
}

// contextWithTrace returns ctx continuing whatever trace the message's headers
// carry. Headers without a `traceparent` yield ctx unchanged, which starts a
// new trace — correct for a record published by something that does not
// propagate context.
func contextWithTrace(ctx context.Context, headers map[string][]byte) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	carrier := make(propagation.MapCarrier, len(headers))
	for k, v := range headers {
		carrier[k] = string(v)
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}
