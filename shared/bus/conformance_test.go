package bus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/pulsetrace/shared/models"
)

// The conformance suite.
//
// # Why this file is the real deliverable of P1.2
//
// Two implementations of one interface are two products unless something forces
// them to agree. The compiler checks the *shape* of the port and nothing about
// its meaning — a transport that dropped every other record, or delivered out of
// order, or lost the trace context, would satisfy `bus.Bus` perfectly.
//
// So every assertion here runs against both. It is the only mechanism that keeps
// lite and cluster semantically identical for the life of the product rather
// than merely similar on the day they were written, and the only place a future
// change to one implementation is forced to confront the other.
//
// The Kafka side is skipped without a broker (KAFKA_CONFORMANCE=1 with
// KAFKA_BROKERS set). That is a real gap and it is named rather than hidden: on
// a developer machine this file proves the in-process implementation satisfies
// the contract, and proves the *contract itself* is executable. CI with a broker
// is what makes it prove both.

// busFactory builds a bus and returns it with a cleanup.
type busFactory struct {
	name string
	make func(t *testing.T) Bus
}

func conformanceBuses(t *testing.T) []busFactory {
	t.Helper()
	buses := []busFactory{{
		name: "in-process",
		make: func(t *testing.T) Bus {
			b, err := NewInProcessBus(t.TempDir(), InProcessOptions{SyncInterval: -1})
			if err != nil {
				t.Fatalf("in-process bus: %v", err)
			}
			t.Cleanup(func() { b.Close() })
			return b
		},
	}}

	if os.Getenv("KAFKA_CONFORMANCE") != "" {
		buses = append(buses, busFactory{
			name: "kafka",
			make: func(t *testing.T) Bus {
				b, err := NewKafkaBus()
				if err != nil {
					t.Skipf("kafka unavailable: %v", err)
				}
				t.Cleanup(func() { b.Close() })
				return b
			},
		})
	}
	return buses
}

// uniqueTopic keeps parallel runs and repeated Kafka runs from colliding.
func uniqueTopic(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("conformance-%d-%d", time.Now().UnixNano(), os.Getpid())
}

// forEachBus runs one assertion against every implementation.
func forEachBus(t *testing.T, name string, fn func(t *testing.T, b Bus)) {
	t.Helper()
	for _, f := range conformanceBuses(t) {
		t.Run(name+"/"+f.name, func(t *testing.T) { fn(t, f.make(t)) })
	}
}

// ── The contract ─────────────────────────────────────────────────────────────

// Everything published must be delivered. This is the assertion the whole
// package exists for: a transport may be slow, but it may not lose a record it
// accepted.
func TestConformancePublishedRecordsAreDelivered(t *testing.T) {
	forEachBus(t, "delivery", func(t *testing.T, b Bus) {
		topic := uniqueTopic(t)
		values := seq(25)
		got := runWithSubscription(t, b, topic, "g1", len(values), func() {
			publishAll(t, b, topic, values)
		})
		if len(got) != len(values) {
			t.Fatalf("delivered %d of %d published records", len(got), len(values))
		}
	})
}

// Per-topic order must be preserved. Out-of-order delivery turns a causal log
// into a set, and every consumer here reconstructs sequence from it.
func TestConformanceOrderIsPreservedPerTopic(t *testing.T) {
	forEachBus(t, "ordering", func(t *testing.T, b Bus) {
		topic := uniqueTopic(t)
		values := seq(25)
		got := runWithSubscription(t, b, topic, "g1", len(values), func() {
			publishAll(t, b, topic, values)
		})
		// Assert the count first. Without this the loop below runs zero times
		// when nothing arrives and the test passes vacuously — which is exactly
		// what it did against Kafka before the start-position divergence was
		// found.
		if len(got) != len(values) {
			t.Fatalf("delivered %d of %d records; ordering cannot be judged", len(got), len(values))
		}
		for i, m := range got {
			if want := values[i]; string(m.Value) != want {
				t.Fatalf("record %d = %q, want %q", i, m.Value, want)
			}
		}
	})
}

// A record's key, value and topic must survive the round trip unchanged.
func TestConformanceKeyAndValueSurvive(t *testing.T) {
	forEachBus(t, "payload", func(t *testing.T, b Bus) {
		topic := uniqueTopic(t)
		got := runWithSubscription(t, b, topic, "g1", 1, func() {
			if err := b.Publish(context.Background(), topic, "cart-service", []byte(`{"level":"ERROR"}`)); err != nil {
				t.Errorf("publish: %v", err)
			}
		})
		if len(got) != 1 {
			t.Fatalf("got %d messages, want 1", len(got))
		}
		if got[0].Key != "cart-service" {
			t.Errorf("Key = %q, want cart-service", got[0].Key)
		}
		if string(got[0].Value) != `{"level":"ERROR"}` {
			t.Errorf("Value = %q", got[0].Value)
		}
		if got[0].Topic != topic {
			t.Errorf("Topic = %q, want %q", got[0].Topic, topic)
		}
	})
}

// PublishBatch must deliver every entry as an individual message.
func TestConformanceBatchDeliversEveryEntry(t *testing.T) {
	forEachBus(t, "batch", func(t *testing.T, b Bus) {
		topic := uniqueTopic(t)
		const n = 10
		entries := make([]*models.LogEntry, n)
		for i := range entries {
			entries[i] = &models.LogEntry{
				ID:          fmt.Sprintf("id-%03d", i),
				ServiceName: "cart-service",
				Message:     fmt.Sprintf("m%03d", i),
			}
		}
		got := runWithSubscription(t, b, topic, "g1", n, func() {
			if err := b.PublishBatch(context.Background(), topic, entries); err != nil {
				t.Errorf("publish batch: %v", err)
			}
		})
		if len(got) != n {
			t.Fatalf("batch of %d delivered %d", n, len(got))
		}
	})
}

// An empty batch is a no-op, not an error and not an empty record.
func TestConformanceEmptyBatchIsANoOp(t *testing.T) {
	forEachBus(t, "empty-batch", func(t *testing.T, b Bus) {
		if err := b.PublishBatch(context.Background(), uniqueTopic(t), nil); err != nil {
			t.Errorf("empty batch returned %v, want nil", err)
		}
	})
}

// Trace context must survive the transport, so a consumer's span continues the
// producer's trace regardless of which implementation carried it. This is the
// property that would silently regress in a second implementation.
func TestConformanceTraceContextSurvives(t *testing.T) {
	forEachBus(t, "trace", func(t *testing.T, b Bus) {
		topic := uniqueTopic(t)
		ctx, want := tracedContext(t)
		got := runWithSubscription(t, b, topic, "g1", 1, func() {
			if err := b.Publish(ctx, topic, "k", []byte("v")); err != nil {
				t.Errorf("publish: %v", err)
			}
		})
		if len(got) != 1 {
			t.Fatalf("got %d messages, want 1", len(got))
		}
		if string(got[0].Headers["traceparent"]) == "" {
			t.Fatalf("no traceparent header survived: %v", got[0].Headers)
		}
		if gotTrace := traceIDFromHeaders(got[0].Headers); gotTrace != want {
			t.Errorf("trace id = %s, want %s — the consumer would start a new trace", gotTrace, want)
		}
	})
}

// A never-committed group starts at the end on both transports.
//
// This is the divergence the suite found: Kafka is configured OffsetNewest, so
// records published before a new group joins are not delivered to it. The
// in-process bus read from zero until this was pinned, which made it a
// different product rather than a drop-in replacement.
func TestConformanceNewGroupStartsAtTheEnd(t *testing.T) {
	forEachBus(t, "start-position", func(t *testing.T, b Bus) {
		topic := uniqueTopic(t)
		ensureTopic(t, b, topic)
		// Published before anyone subscribes: must not be delivered.
		publishAll(t, b, topic, []string{"before-1", "before-2"})

		got := runWithSubscription(t, b, topic, "fresh-group", 1, func() {
			publishAll(t, b, topic, []string{"after"})
		})
		// Assert something arrived before judging what it was — otherwise this
		// passes on a transport that delivered nothing, which is how it first
		// reported green against Kafka.
		if len(got) == 0 {
			t.Fatal("the new group received nothing at all")
		}
		for _, m := range got {
			if v := string(m.Value); v != "after" {
				t.Errorf("a new group received %q, published before it joined", v)
			}
		}
	})
}

// Subscribe must reject a nil handler rather than deliver into nothing.
func TestConformanceSubscribeRejectsNilHandler(t *testing.T) {
	forEachBus(t, "nil-handler", func(t *testing.T, b Bus) {
		if _, err := b.Subscribe("g", []string{uniqueTopic(t)}, nil); err == nil {
			t.Error("expected an error for a nil handler")
		}
	})
}

// A handler that errors must not stop the subscription: one unreadable record
// cannot wedge a topic forever.
func TestConformanceHandlerErrorDoesNotWedgeTheTopic(t *testing.T) {
	forEachBus(t, "poison-pill", func(t *testing.T, b Bus) {
		topic := uniqueTopic(t)
		values := seq(5)
		ensureTopic(t, b, topic)

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		var (
			mu   sync.Mutex
			seen []string
		)
		ready := make(chan struct{})
		var readyOne sync.Once
		sub, err := b.Subscribe("g1", []string{topic}, func(_ context.Context, m Message) error {
			if string(m.Value) == readyPrefix {
				readyOne.Do(func() { close(ready) })
				return nil
			}
			mu.Lock()
			seen = append(seen, string(m.Value))
			n := len(seen)
			mu.Unlock()
			if n >= len(values) {
				cancel()
			}
			return errors.New("handler always fails")
		})
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		defer sub.Close()

		done := make(chan struct{})
		go func() { defer close(done); _ = sub.Run(ctx) }()
		awaitSubscription(t, b, topic, ready)
		publishAll(t, b, topic, values)
		<-done

		mu.Lock()
		defer mu.Unlock()
		if len(seen) < len(values) {
			t.Errorf("saw %d of %d records; a failing handler wedged the topic", len(seen), len(values))
		}
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

// readyPrefix marks the handshake records used to detect a live subscription.
const readyPrefix = "__ready__"

// awaitSubscription blocks until the subscription is actually receiving.
//
// # Why this is a handshake and not a sleep
//
// A fixed sleep was the first attempt and it was wrong in a way worth
// recording: measured against the live broker, Kafka's fresh-group rebalance
// completes at 3–4 seconds, and the sleep was 3. Records published before the
// group is assigned land below its start offset and are never delivered, so the
// suite reported the Kafka side as losing every record when it was simply being
// published to before it was listening.
//
// Any fixed value is a guess about somebody else's rebalance. Publishing a
// sentinel until one comes back measures the thing directly, is as fast as the
// transport allows, and is identical for an implementation that is ready
// immediately.
func awaitSubscription(t *testing.T, b Bus, topic string, ready <-chan struct{}) {
	t.Helper()
	deadline := time.After(45 * time.Second)
	for i := 0; ; i++ {
		select {
		case <-ready:
			return
		case <-deadline:
			t.Fatalf("subscription on %s never became live", topic)
		default:
		}
		if err := b.Publish(context.Background(), topic, "ready", []byte(readyPrefix)); err != nil {
			t.Fatalf("handshake publish: %v", err)
		}
		select {
		case <-ready:
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func seq(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("v%03d", i)
	}
	return out
}

// ensureTopic makes the topic exist before anyone subscribes to it.
//
// Not a workaround — it mirrors production. Topics are created by the setup
// container before any service starts, and Kafka gives a consumer group that
// joins a *non-existent* topic no partition assignment at all: it then sits
// idle until the next metadata refresh, which is minutes away. Subscribing to a
// topic that does not exist yet is a test artefact, not a transport behaviour,
// and leaving it in place produced a suite that reported the Kafka side as
// broken when it was fine.
//
// The warm-up record lands before any group joins, so by the start-position
// contract it is not delivered to anyone.
func ensureTopic(t *testing.T, b Bus, topic string) {
	t.Helper()
	if err := b.Publish(context.Background(), topic, "warmup", []byte("warmup")); err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
	time.Sleep(topicSettle)
}

// topicSettle is how long to wait for a newly created topic to be visible to a
// joining consumer group.
const topicSettle = 2 * time.Second

func publishAll(t *testing.T, b Bus, topic string, values []string) {
	t.Helper()
	for _, v := range values {
		if err := b.Publish(context.Background(), topic, "k", []byte(v)); err != nil {
			t.Errorf("publish %q: %v", v, err)
			return
		}
	}
}

// runWithSubscription subscribes, waits for the subscription to be live, runs
// publish, and collects up to want messages.
//
// Subscribe-then-publish is part of the contract, not a testing convenience: a
// group with no committed offset starts at the end on both transports, so a
// test that publishes first is asserting replay semantics neither one promises.
func runWithSubscription(t *testing.T, b Bus, topic, group string, want int, publish func()) []Message {
	t.Helper()
	ensureTopic(t, b, topic)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		msgs     []Message
		ready    = make(chan struct{})
		readyOne sync.Once
	)
	sub, err := b.Subscribe(group, []string{topic}, func(_ context.Context, m Message) error {
		if string(m.Value) == readyPrefix {
			readyOne.Do(func() { close(ready) })
			return nil
		}
		mu.Lock()
		msgs = append(msgs, m)
		n := len(msgs)
		mu.Unlock()
		if n >= want {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	done := make(chan struct{})
	go func() { defer close(done); _ = sub.Run(ctx) }()

	awaitSubscription(t, b, topic, ready)
	publish()
	<-done

	mu.Lock()
	defer mu.Unlock()
	return append([]Message(nil), msgs...)
}

// tracedContext returns a context carrying a live span, plus its trace id.
func tracedContext(t *testing.T) (context.Context, trace.TraceID) {
	t.Helper()
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	ctx, span := otel.Tracer("conformance").Start(context.Background(), "produce")
	t.Cleanup(func() { span.End() })
	return ctx, span.SpanContext().TraceID()
}

// traceIDFromHeaders reads the trace id a consumer would continue.
func traceIDFromHeaders(h map[string][]byte) trace.TraceID {
	return trace.SpanContextFromContext(contextWithTrace(context.Background(), h)).TraceID()
}
