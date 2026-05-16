package kafka

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/IBM/sarama"
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

	// Retry connecting to Kafka on startup — brokers may not be ready yet.
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

// Publish sends a JSON-encoded message to the given topic.
// The key is used for partition routing (e.g. service name).
func (p *Producer) Publish(topic, key string, value []byte) error {
	msg := &sarama.ProducerMessage{
		Topic: topic,
		Key:   sarama.StringEncoder(key),
		Value: sarama.ByteEncoder(value),
	}
	partition, offset, err := p.producer.SendMessage(msg)
	if err != nil {
		return fmt.Errorf("failed to publish to topic %q: %w", topic, err)
	}
	log.Printf("kafka: published to %s partition=%d offset=%d key=%s", topic, partition, offset, key)
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
