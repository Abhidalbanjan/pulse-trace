package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/IBM/sarama"
	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/telemetry"
)

// Producer wraps a synchronous Sarama producer.
type Producer struct {
	producer sarama.SyncProducer
}

// NewProducer creates a new synchronous Kafka producer.
// Brokers are read from the KAFKA_BROKERS env variable (comma-separated),
// defaulting to "localhost:9092".
func NewProducer() (*Producer, error) {
	brokers := brokerList()

	cfg := sarama.NewConfig()
	cfg.Version = sarama.V3_6_0_0
	cfg.Producer.Return.Successes = true
	cfg.Producer.Return.Errors = true
	cfg.Producer.RequiredAcks = sarama.WaitForAll
	cfg.Producer.Retry.Max = 5
	cfg.Producer.Retry.Backoff = 200 * time.Millisecond

	const maxRetries = 15
	var (
		p   sarama.SyncProducer
		err error
	)
	for i := range maxRetries {
		p, err = sarama.NewSyncProducer(brokers, cfg)
		if err == nil {
			log.Printf("kafka producer connected to %v (attempt %d)", brokers, i+1)
			return &Producer{producer: p}, nil
		}
		log.Printf("kafka not ready, retrying in 3s (attempt %d/%d): %v", i+1, maxRetries, err)
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("could not connect to kafka after %d attempts: %w", maxRetries, err)
}

// Publish sends a message to the given topic without trace context.
func (p *Producer) Publish(topic, key string, value []byte) error {
	return p.PublishWithContext(context.Background(), topic, key, value)
}

// PublishWithContext sends a message to the given topic, injecting the W3C
// trace context from ctx into the Kafka message headers so downstream
// consumers can continue the distributed trace.
func (p *Producer) PublishWithContext(ctx context.Context, topic, key string, value []byte) error {
	msg := &sarama.ProducerMessage{
		Topic: topic,
		Key:   sarama.StringEncoder(key),
		Value: sarama.ByteEncoder(value),
	}

	// Inject trace context into Kafka headers (W3C traceparent / tracestate).
	telemetry.InjectKafkaHeaders(ctx, msg)

	partition, offset, err := p.producer.SendMessage(msg)
	if err != nil {
		return fmt.Errorf("failed to publish to topic %q: %w", topic, err)
	}
	log.Printf("kafka: published to %s partition=%d offset=%d key=%s", topic, partition, offset, key)
	return nil
}

// PublishBatch sends multiple messages to Kafka in a single optimized request.
func (p *Producer) PublishBatch(ctx context.Context, topic string, entries []*models.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	msgs := make([]*sarama.ProducerMessage, len(entries))
	for i, entry := range entries {
		payload, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("failed to marshal entry for Kafka batch: %w", err)
		}

		msgs[i] = &sarama.ProducerMessage{
			Topic: topic,
			Key:   sarama.StringEncoder(entry.ServiceName),
			Value: sarama.ByteEncoder(payload),
		}

		// Inject the current span context so downstream consumers can continue the distributed trace
		telemetry.InjectKafkaHeaders(ctx, msgs[i])
	}

	if err := p.producer.SendMessages(msgs); err != nil {
		return fmt.Errorf("failed to publish batch to topic %q: %w", topic, err)
	}
	return nil
}

// Close shuts down the producer gracefully.
func (p *Producer) Close() error {
	return p.producer.Close()
}

func brokerList() []string {
	v := os.Getenv("KAFKA_BROKERS")
	if v == "" {
		return []string{"localhost:9092"}
	}
	return strings.Split(v, ",")
}
