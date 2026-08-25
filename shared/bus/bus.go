// Package bus is the message-transport port (P1.1).
//
// # Why this exists
//
// PulseTrace runs 23 containers against OpenObserve's 2 — measured, and the
// largest single adoption barrier we have. Kafka plus ZooKeeper is two of those
// containers and the reason several others exist. The plan's answer is not to
// drop Kafka but to stop *requiring* it: one interface, two implementations, the
// same semantics, so the same code is either a single-container product or the
// cluster we run today.
//
// This package is step one of that: the seam. It wraps today's Kafka client
// verbatim and changes no behaviour. The in-process implementation with a
// durable WAL is P1.2, and it only becomes possible once every call site talks
// to an interface rather than to sarama.
//
// # What this package is really for
//
// Sarama currently leaks into six services: every consumer's `Handle` takes a
// `*sarama.ConsumerMessage`, so every one of them has a compile-time dependency
// on a specific Kafka client. That is what makes a second transport impossible,
// and it is the actual deliverable here — not the interface, but the fact that
// after this change `sarama` appears in exactly one file per implementation.
package bus

import (
	"context"
	"errors"
	"time"

	"github.com/pulsetrace/shared/models"
)

// ErrBusUnavailable is returned when the transport cannot be reached.
//
// It is a distinct error because callers must be able to tell "the bus is down"
// from "this message was rejected": the first is retryable and belongs in a 503
// or a 429, the second is not. The gateway maps it to a status code rather than
// logging and continuing — telemetry dropped silently corrupts every count
// downstream of it, and a dropped record is not recoverable later.
var ErrBusUnavailable = errors.New("bus: transport unavailable")

// Message is one record delivered to a Handler.
//
// Deliberately not a Kafka type. Partition and Offset are kept even though they
// read as Kafka concepts because consumers use them for span attributes and log
// lines, and every durable log has an equivalent — the in-process bus fills them
// from its WAL segment and position. A transport with no such notion may leave
// them zero.
type Message struct {
	Topic     string
	Key       string
	Value     []byte
	Timestamp time.Time
	Partition int32
	Offset    int64
	// Headers carries the record's metadata, including the W3C `traceparent`.
	// The trace context is *also* already applied to the ctx a Handler receives;
	// this is here for transports or consumers that need the raw values.
	Headers map[string][]byte
}

// Handler processes one message.
//
// The ctx it receives already carries the message's remote trace context, so a
// span started from it continues the producer's trace with no work by the
// caller. That extraction used to be the first line of every consumer, which
// meant every consumer imported sarama to do it.
//
// Returning an error does not stop the subscription: it is logged and the
// message is marked processed, so one unreadable record cannot wedge a
// partition. That is the existing Kafka behaviour, preserved deliberately —
// changing poison-pill handling is a separate decision from moving the seam.
type Handler func(ctx context.Context, m Message) error

// Bus is the transport port.
type Bus interface {
	// Publish sends one message, injecting the trace context from ctx.
	Publish(ctx context.Context, topic, key string, value []byte) error
	// PublishBatch sends many log entries as one request.
	PublishBatch(ctx context.Context, topic string, entries []*models.LogEntry) error
	// Subscribe joins a consumer group. It connects eagerly and returns an
	// error if it cannot, so a caller can fail fast at startup; consumption
	// begins when the returned Subscription is Run.
	Subscribe(group string, topics []string, h Handler) (Subscription, error)
	Close() error
}

// Subscription is a joined consumer group that has not started consuming yet.
//
// # Why this is two calls and not one
//
// The plan sketches a single blocking `Subscribe(ctx, group, topics, h)`. That
// reads better and is wrong here: every service today constructs its consumer
// group *before* serving HTTP and calls log.Fatalf if the broker is
// unreachable. Folding connect into a blocking call moves that failure into a
// goroutine, so a broker outage at boot would leave the service up, healthy,
// serving HTTP, and consuming nothing — with the failure visible only in a log
// line. Silent non-consumption is precisely what this package's own doc comment
// says it must never do, so the two-phase lifecycle stays.
type Subscription interface {
	// Run consumes until ctx is cancelled. It blocks.
	Run(ctx context.Context) error
	Close() error
}
