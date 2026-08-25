package bus

// The Kafka implementation of the port.
//
// This file is the *only* place in the repository, outside shared/kafka itself,
// that is allowed to know Kafka exists. It wraps the existing Producer and
// ConsumerGroup verbatim rather than reimplementing them: the acceptance proof
// for this slice is that behaviour did not change, and the cheapest way to be
// sure of that is to not rewrite the code that was working.

import (
	"context"
	"errors"
	"fmt"

	"github.com/IBM/sarama"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/models"
)

// KafkaBus is the cluster-mode transport.
type KafkaBus struct {
	producer *kafka.Producer
}

// NewKafkaBus connects a producer. The retry/backoff behaviour is the existing
// client's; a failure here is wrapped as ErrBusUnavailable so callers can tell
// an unreachable broker from a misconfigured one.
func NewKafkaBus() (*KafkaBus, error) {
	p, err := kafka.NewProducer()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBusUnavailable, err)
	}
	return &KafkaBus{producer: p}, nil
}

// NewKafkaBusWith wraps an already-constructed producer. Used by services that
// build the producer themselves during the migration, and by tests.
func NewKafkaBusWith(p *kafka.Producer) *KafkaBus { return &KafkaBus{producer: p} }

func (b *KafkaBus) Publish(ctx context.Context, topic, key string, value []byte) error {
	if b == nil || b.producer == nil {
		return ErrBusUnavailable
	}
	return b.producer.PublishWithContext(ctx, topic, key, value)
}

func (b *KafkaBus) PublishBatch(ctx context.Context, topic string, entries []*models.LogEntry) error {
	if b == nil || b.producer == nil {
		return ErrBusUnavailable
	}
	return b.producer.PublishBatch(ctx, topic, entries)
}

func (b *KafkaBus) Subscribe(group string, topics []string, h Handler) (Subscription, error) {
	if h == nil {
		return nil, errors.New("bus: Subscribe requires a handler")
	}
	// The adapter, not the consumer, turns a Kafka record into a Message and
	// applies its trace context. This is the line that lets every consumer stop
	// importing sarama.
	cg, err := kafka.NewConsumerGroup(group, topics, func(msg *sarama.ConsumerMessage) error {
		m := fromSarama(msg)
		// Extraction goes through the port's header map, not a sarama-typed
		// helper, so the in-process transport inherits identical behaviour.
		return h(contextWithTrace(context.Background(), m.Headers), m)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBusUnavailable, err)
	}
	return &kafkaSubscription{group: cg}, nil
}

func (b *KafkaBus) Close() error {
	if b == nil || b.producer == nil {
		return nil
	}
	return b.producer.Close()
}

type kafkaSubscription struct{ group *kafka.ConsumerGroup }

func (s *kafkaSubscription) Run(ctx context.Context) error { return s.group.Start(ctx) }
func (s *kafkaSubscription) Close() error                  { return s.group.Close() }

// fromSarama converts a Kafka record to the transport-neutral Message.
func fromSarama(msg *sarama.ConsumerMessage) Message {
	m := Message{
		Topic:     msg.Topic,
		Value:     msg.Value,
		Timestamp: msg.Timestamp,
		Partition: msg.Partition,
		Offset:    msg.Offset,
	}
	if len(msg.Key) > 0 {
		m.Key = string(msg.Key)
	}
	if len(msg.Headers) > 0 {
		m.Headers = make(map[string][]byte, len(msg.Headers))
		for _, hdr := range msg.Headers {
			if hdr == nil {
				continue
			}
			m.Headers[string(hdr.Key)] = hdr.Value
		}
	}
	return m
}

// compile-time proof the adapter satisfies the port.
var (
	_ Bus          = (*KafkaBus)(nil)
	_ Subscription = (*kafkaSubscription)(nil)
)
