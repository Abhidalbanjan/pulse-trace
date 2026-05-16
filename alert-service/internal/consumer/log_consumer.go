package consumer

import (
	"context"
	"encoding/json"
	"log"

	"github.com/IBM/sarama"
	"github.com/pulsetrace/alert-service/internal/repository"
	"github.com/pulsetrace/shared/models"
)

// alertLevels defines which log levels trigger an alert.
var alertLevels = map[models.LogLevel]bool{
	models.LogLevelError: true,
	models.LogLevelFatal: true,
}

// LogConsumer processes log events from Kafka and creates alerts for
// ERROR and FATAL entries.
type LogConsumer struct {
	repo *repository.AlertRepository
}

func NewLogConsumer(repo *repository.AlertRepository) *LogConsumer {
	return &LogConsumer{repo: repo}
}

// Handle is the MessageHandler passed to kafka.ConsumerGroup.
func (c *LogConsumer) Handle(msg *sarama.ConsumerMessage) error {
	var entry models.LogEntry
	if err := json.Unmarshal(msg.Value, &entry); err != nil {
		log.Printf("log_consumer: failed to unmarshal message at offset %d: %v", msg.Offset, err)
		return nil // don't retry malformed messages
	}

	if !alertLevels[entry.Level] {
		return nil // not an alertable level
	}

	alert, err := c.repo.Insert(context.Background(), &entry)
	if err != nil {
		log.Printf("log_consumer: failed to store alert for log %s: %v", entry.ID, err)
		return err
	}

	log.Printf("log_consumer: alert created id=%s service=%s level=%s", alert.ID, alert.ServiceName, alert.Level)
	return nil
}
