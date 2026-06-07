package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"github.com/pulsetrace/log-service/internal/repository"
	"github.com/pulsetrace/shared/models"
)

// ClickHouseConsumer consumes logs from Kafka and batches them into ClickHouse.
type ClickHouseConsumer struct {
	repo          *repository.ClickHouseLogRepository
	logQueue      chan *models.LogEntry
	batchSize     int
	flushInterval time.Duration
	workersWg     sync.WaitGroup
	closeChan     chan struct{}
}

// NewClickHouseConsumer initializes the batch writer.
func NewClickHouseConsumer(repo *repository.ClickHouseLogRepository) *ClickHouseConsumer {
	c := &ClickHouseConsumer{
		repo:          repo,
		logQueue:      make(chan *models.LogEntry, 100000), // 100k shock absorber buffer
		batchSize:     10000,                               // Up to 10k logs in a single batch
		flushInterval: 100 * time.Millisecond,
		closeChan:     make(chan struct{}),
	}

	// Spin up 4 concurrent writers
	for i := 0; i < 4; i++ {
		c.workersWg.Add(1)
		go c.worker(i)
	}

	log.Printf("clickhouse-consumer: initialized ClickHouse Kafka batch consumer (4 workers, batch=%d, latency=%v)", c.batchSize, c.flushInterval)
	return c
}

// Handle implements the sarama MessageHandler function signature.
func (c *ClickHouseConsumer) Handle(msg *sarama.ConsumerMessage) error {
	var entry models.LogEntry
	if err := json.Unmarshal(msg.Value, &entry); err != nil {
		return fmt.Errorf("failed to unmarshal log message: %w", err)
	}

	select {
	case c.logQueue <- &entry:
		return nil
	case <-c.closeChan:
		return errors.New("clickhouse consumer is shutting down")
	}
}

// worker drains the channel and writes batches
func (c *ClickHouseConsumer) worker(id int) {
	defer c.workersWg.Done()

	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()

	var batch []*models.LogEntry

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := c.repo.BulkInsert(ctx, batch); err != nil {
			log.Printf("clickhouse-consumer [worker-%d]: error bulk inserting %d logs to ClickHouse: %v", id, len(batch), err)
		} else {
			log.Printf("clickhouse-consumer [worker-%d]: successfully inserted %d logs into ClickHouse", id, len(batch))
		}
		batch = nil
	}

	for {
		select {
		case entry, ok := <-c.logQueue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, entry)
			if len(batch) >= c.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// Close gracefully shuts down the consumer group processing.
func (c *ClickHouseConsumer) Close() {
	log.Println("closing clickhouse-consumer log queues...")
	close(c.closeChan)
	close(c.logQueue)
	c.workersWg.Wait()
	log.Println("clickhouse-consumer shutdown complete.")
}
