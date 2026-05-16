package kafka

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/IBM/sarama"
)

// MessageHandler is called for every message consumed from Kafka.
// Returning an error does not stop the consumer — it logs and continues.
type MessageHandler func(msg *sarama.ConsumerMessage) error

// ConsumerGroup wraps a Sarama consumer group.
type ConsumerGroup struct {
	group   sarama.ConsumerGroup
	topics  []string
	handler MessageHandler
}

// NewConsumerGroup creates a consumer group that reads from the given topics.
// The group ID and broker list are read from KAFKA_GROUP_ID and KAFKA_BROKERS env vars.
func NewConsumerGroup(groupID string, topics []string, handler MessageHandler) (*ConsumerGroup, error) {
	brokers := brokerList()

	cfg := sarama.NewConfig()
	cfg.Version = sarama.V3_6_0_0
	cfg.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{sarama.NewBalanceStrategyRoundRobin()}
	cfg.Consumer.Offsets.Initial = sarama.OffsetNewest
	cfg.Consumer.Return.Errors = true

	if v := os.Getenv("KAFKA_GROUP_ID"); v != "" {
		groupID = v
	}

	const maxRetries = 15
	var (
		cg  sarama.ConsumerGroup
		err error
	)
	for i := range maxRetries {
		cg, err = sarama.NewConsumerGroup(brokers, groupID, cfg)
		if err == nil {
			log.Printf("kafka consumer group %q connected to %v (attempt %d)", groupID, brokers, i+1)
			return &ConsumerGroup{group: cg, topics: topics, handler: handler}, nil
		}
		log.Printf("kafka not ready, retrying in 3s (attempt %d/%d): %v", i+1, maxRetries, err)
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("could not connect kafka consumer group after %d attempts: %w", maxRetries, err)
}

// Start begins consuming messages in a blocking loop until ctx is cancelled.
func (c *ConsumerGroup) Start(ctx context.Context) error {
	h := &consumerGroupHandler{handler: c.handler}
	for {
		if err := c.group.Consume(ctx, c.topics, h); err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			log.Printf("kafka consumer error: %v — restarting in 2s", err)
			time.Sleep(2 * time.Second)
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// Close shuts down the consumer group.
func (c *ConsumerGroup) Close() error {
	return c.group.Close()
}

// consumerGroupHandler implements sarama.ConsumerGroupHandler.
type consumerGroupHandler struct {
	handler MessageHandler
}

func (h *consumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *consumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

func (h *consumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		if err := h.handler(msg); err != nil {
			log.Printf("kafka handler error for offset %d: %v", msg.Offset, err)
		}
		// Mark the message as processed regardless of handler error so we don't
		// get stuck on a poison-pill message.
		session.MarkMessage(msg, "")
	}
	return nil
}
