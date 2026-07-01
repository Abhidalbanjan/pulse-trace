package rabbitmq

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	ExchangeIncidents  = "incidents"
	QueueNotifications = "notifications"
	// Dead-letter queue for failed notifications.
	QueueNotificationsDLQ = "notifications.dlq"
)

// Publisher wraps an AMQP channel for publishing messages.
type Publisher struct {
	conn    *amqp.Connection
	channel *amqp.Channel
}

// NewPublisher creates a Publisher connected to RabbitMQ.
// The URL is read from RABBITMQ_URL env var, defaulting to localhost.
func NewPublisher() (*Publisher, error) {
	url := rabbitmqURL()

	const maxRetries = 15
	var (
		conn *amqp.Connection
		err  error
	)
	for i := range maxRetries {
		conn, err = amqp.Dial(url)
		if err == nil {
			log.Printf("rabbitmq publisher connected (attempt %d)", i+1)
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

	return &Publisher{conn: conn, channel: ch}, nil
}

// Publish sends a JSON payload to the notifications queue.
func (p *Publisher) Publish(ctx context.Context, routingKey string, body []byte) error {
	return p.channel.PublishWithContext(ctx,
		ExchangeIncidents, // exchange
		routingKey,        // routing key
		false,             // mandatory
		false,             // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		},
	)
}

// Close shuts down the publisher.
func (p *Publisher) Close() {
	if p.channel != nil {
		p.channel.Close()
	}
	if p.conn != nil {
		p.conn.Close()
	}
}

// declareTopology ensures the exchange, queues, and bindings exist.
func declareTopology(ch *amqp.Channel) error {
	// Durable topic exchange — survives broker restarts.
	if err := ch.ExchangeDeclare(ExchangeIncidents, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("failed to declare exchange: %w", err)
	}

	// Dead-letter queue first (referenced by main queue args).
	dlqArgs := amqp.Table{
		"x-queue-type": "quorum",
	}
	_, err := ch.QueueDeclare(QueueNotificationsDLQ, true, false, false, false, dlqArgs)
	if err != nil {
		return fmt.Errorf("failed to declare DLQ: %w", err)
	}

	// Main notifications queue with DLQ and per-message TTL (24h).
	args := amqp.Table{
		"x-queue-type":              "quorum",
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": QueueNotificationsDLQ,
		"x-message-ttl":             int32(86400000), // 24h in ms
	}
	_, err = ch.QueueDeclare(QueueNotifications, true, false, false, false, args)
	if err != nil {
		return fmt.Errorf("failed to declare notifications queue: %w", err)
	}

	// Bind queue to exchange with wildcard routing key.
	if err := ch.QueueBind(QueueNotifications, "#", ExchangeIncidents, false, nil); err != nil {
		return fmt.Errorf("failed to bind queue: %w", err)
	}

	return nil
}

func rabbitmqURL() string {
	if v := os.Getenv("RABBITMQ_URL"); v != "" {
		return v
	}
	return "amqp://guest:guest@localhost:5672/"
}
