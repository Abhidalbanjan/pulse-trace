package rabbitmq

import (
	"context"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// MessageHandler is called for every message delivered from RabbitMQ.
// Returning an error nacks the message (it goes to the DLQ after max retries).
type MessageHandler func(ctx context.Context, body []byte) error

// Consumer wraps an AMQP channel for consuming messages.
type Consumer struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	queue   string
}

// NewConsumer creates a Consumer connected to RabbitMQ.
func NewConsumer(queue string) (*Consumer, error) {
	url := rabbitmqURL()

	const maxRetries = 15
	var (
		conn *amqp.Connection
		err  error
	)
	for i := range maxRetries {
		conn, err = amqp.Dial(url)
		if err == nil {
			log.Printf("rabbitmq consumer connected (attempt %d)", i+1)
			break
		}
		log.Printf("rabbitmq not ready, retrying in 3s (attempt %d/%d): %v", i+1, maxRetries, err)
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("could not connect to rabbitmq after %d attempts: %w", maxRetries, err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to open rabbitmq channel: %w", err)
	}

	if err := declareTopology(ch); err != nil {
		ch.Close()
		conn.Close()
		return nil, err
	}

	// Prefetch 1 so workers process one message at a time.
	if err := ch.Qos(1, 0, false); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("failed to set QoS: %w", err)
	}

	return &Consumer{conn: conn, channel: ch, queue: queue}, nil
}

// Start begins consuming messages in a blocking loop until ctx is cancelled.
func (c *Consumer) Start(ctx context.Context, handler MessageHandler) error {
	msgs, err := c.channel.Consume(
		c.queue,
		"",    // consumer tag (auto-generated)
		false, // auto-ack — we ack manually after successful processing
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to start consuming: %w", err)
	}

	log.Printf("rabbitmq consumer started on queue %q", c.queue)

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-msgs:
			if !ok {
				return fmt.Errorf("rabbitmq channel closed")
			}
			if err := handler(ctx, msg.Body); err != nil {
				log.Printf("rabbitmq handler error: %v — nacking message", err)
				_ = msg.Nack(false, false) // send to DLQ
			} else {
				_ = msg.Ack(false)
			}
		}
	}
}

// Close shuts down the consumer.
func (c *Consumer) Close() {
	if c.channel != nil {
		c.channel.Close()
	}
	if c.conn != nil {
		c.conn.Close()
	}
}
