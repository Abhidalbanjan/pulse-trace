package bus

import (
	"context"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/pulsetrace/shared/telemetry"
)

// fromSarama is the conversion every consumer's input now passes through. A
// field dropped here silently disappears from six services at once, so each is
// asserted rather than spot-checked.
func TestFromSaramaPreservesEveryField(t *testing.T) {
	ts := time.Date(2026, 8, 25, 10, 30, 0, 0, time.UTC)
	msg := &sarama.ConsumerMessage{
		Topic:     "logs",
		Key:       []byte("cart-service"),
		Value:     []byte(`{"id":"abc"}`),
		Timestamp: ts,
		Partition: 7,
		Offset:    12345,
		Headers: []*sarama.RecordHeader{
			{Key: []byte("traceparent"), Value: []byte("00-x-y-01")},
			{Key: []byte("custom"), Value: []byte("v")},
		},
	}

	m := fromSarama(msg)

	if m.Topic != "logs" {
		t.Errorf("Topic = %q", m.Topic)
	}
	if m.Key != "cart-service" {
		t.Errorf("Key = %q", m.Key)
	}
	if string(m.Value) != `{"id":"abc"}` {
		t.Errorf("Value = %q", m.Value)
	}
	if !m.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", m.Timestamp, ts)
	}
	if m.Partition != 7 {
		t.Errorf("Partition = %d", m.Partition)
	}
	if m.Offset != 12345 {
		t.Errorf("Offset = %d", m.Offset)
	}
	if string(m.Headers["traceparent"]) != "00-x-y-01" {
		t.Errorf("traceparent header lost: %v", m.Headers)
	}
	if string(m.Headers["custom"]) != "v" {
		t.Errorf("custom header lost: %v", m.Headers)
	}
}

// A record with no key and no headers must not produce an empty-string key that
// looks deliberate, or a non-nil empty map that hides "there were none".
func TestFromSaramaHandlesAbsentKeyAndHeaders(t *testing.T) {
	m := fromSarama(&sarama.ConsumerMessage{Topic: "logs", Value: []byte("{}")})
	if m.Key != "" {
		t.Errorf("Key = %q, want empty", m.Key)
	}
	if m.Headers != nil {
		t.Errorf("Headers = %v, want nil when the record carried none", m.Headers)
	}
}

// A nil header entry must not panic the conversion — that would take down a
// consumer for every subsequent message on the partition.
func TestFromSaramaSkipsNilHeaders(t *testing.T) {
	m := fromSarama(&sarama.ConsumerMessage{
		Topic:   "logs",
		Headers: []*sarama.RecordHeader{nil, {Key: []byte("k"), Value: []byte("v")}},
	})
	if string(m.Headers["k"]) != "v" {
		t.Errorf("headers = %v, want the non-nil entry preserved", m.Headers)
	}
}

// The point of moving extraction into the adapter: a Handler's ctx must already
// continue the producer's trace, so no consumer has to import sarama to do it.
//
// Asserted end-to-end through the real propagator rather than by checking that
// a helper was called — the failure being guarded against is a span that
// silently starts a *new* trace, which a mock would not catch.
func TestHandlerContextContinuesTheProducersTrace(t *testing.T) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})

	producerCtx, span := otel.Tracer("test").Start(context.Background(), "produce")
	out := &sarama.ProducerMessage{Topic: "logs"}
	telemetry.InjectKafkaHeaders(producerCtx, out)
	want := span.SpanContext().TraceID()
	span.End()

	if len(out.Headers) == 0 {
		t.Fatal("no headers injected; the propagator is not configured")
	}

	in := &sarama.ConsumerMessage{Topic: "logs", Value: []byte("{}")}
	for _, h := range out.Headers {
		in.Headers = append(in.Headers, &sarama.RecordHeader{Key: h.Key, Value: h.Value})
	}

	// The real path Subscribe takes: convert, then extract from the port's
	// header map. Asserting through these two functions rather than a
	// re-implementation means the test fails if either drifts.
	ctx := contextWithTrace(context.Background(), fromSarama(in).Headers)
	got := trace.SpanContextFromContext(ctx).TraceID()

	if got != want {
		t.Errorf("handler trace id = %s, want %s — the consumer would start a new "+
			"trace instead of continuing the producer's", got, want)
	}
}

func TestSubscribeRejectsNilHandler(t *testing.T) {
	b := &KafkaBus{}
	if _, err := b.Subscribe("g", []string{"logs"}, nil); err == nil {
		t.Error("expected an error for a nil handler")
	}
}

// A zero-value bus must report unavailability rather than panic. Services build
// the producer optionally today (the gateway degrades when Kafka is absent), so
// this path is reachable in production, not only in tests.
func TestZeroBusReportsUnavailableRatherThanPanicking(t *testing.T) {
	var b *KafkaBus
	if err := b.Publish(context.Background(), "logs", "k", []byte("v")); err == nil {
		t.Error("Publish on a nil bus should error")
	}
	if err := b.PublishBatch(context.Background(), "logs", nil); err == nil {
		t.Error("PublishBatch on a nil bus should error")
	}
	if err := b.Close(); err != nil {
		t.Errorf("Close on a nil bus should be a no-op, got %v", err)
	}
}

// A message with no headers must start a new trace rather than panic or inherit
// something unrelated — records published by anything that does not propagate
// context land here.
func TestContextWithTraceToleratesAbsentHeaders(t *testing.T) {
	ctx := contextWithTrace(context.Background(), nil)
	if ctx == nil {
		t.Fatal("nil context returned")
	}
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Error("headerless message produced a valid remote span context")
	}
}
