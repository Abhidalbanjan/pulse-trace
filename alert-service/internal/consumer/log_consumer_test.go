package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/IBM/sarama"
	"github.com/pulsetrace/shared/models"
)

// fakeStore records the entries it's asked to insert and can be told to fail,
// so the consumer's alerting decisions are observable without a database.
type fakeStore struct {
	inserted []*models.LogEntry
	failWith error
}

func (f *fakeStore) Insert(_ context.Context, entry *models.LogEntry) (*models.Alert, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	f.inserted = append(f.inserted, entry)
	return &models.Alert{
		ID:          "alert-" + entry.ID,
		ServiceName: entry.ServiceName,
		Level:       entry.Level,
		TraceID:     entry.TraceID,
	}, nil
}

func msgFor(t *testing.T, entry models.LogEntry) *sarama.ConsumerMessage {
	t.Helper()
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	return &sarama.ConsumerMessage{Topic: "logs", Value: b}
}

func TestHandle_ErrorAndFatalCreateAlerts(t *testing.T) {
	for _, lvl := range []models.LogLevel{models.LogLevelError, models.LogLevelFatal} {
		store := &fakeStore{}
		c := NewLogConsumer(store, nil) // nil producer: publish path is skipped

		if err := c.Handle(msgFor(t, models.LogEntry{ID: "1", ServiceName: "payments", Level: lvl, Message: "boom"})); err != nil {
			t.Fatalf("level %s: Handle returned error: %v", lvl, err)
		}
		if len(store.inserted) != 1 {
			t.Fatalf("level %s: expected 1 alert inserted, got %d", lvl, len(store.inserted))
		}
	}
}

func TestHandle_NonAlertLevelsAreIgnored(t *testing.T) {
	// INFO/WARNING/DEBUG must never create an alert - otherwise every routine
	// log would page on-call and flood the correlation engine.
	for _, lvl := range []models.LogLevel{models.LogLevelInfo, models.LogLevelWarning, models.LogLevelDebug} {
		store := &fakeStore{}
		c := NewLogConsumer(store, nil)

		if err := c.Handle(msgFor(t, models.LogEntry{ID: "1", ServiceName: "s", Level: lvl, Message: "routine"})); err != nil {
			t.Fatalf("level %s: Handle returned error: %v", lvl, err)
		}
		if len(store.inserted) != 0 {
			t.Fatalf("level %s: expected no alert, got %d", lvl, len(store.inserted))
		}
	}
}

func TestHandle_MalformedMessageIsSwallowed(t *testing.T) {
	store := &fakeStore{}
	c := NewLogConsumer(store, nil)

	// A poison message must not be returned as an error (which would make Kafka
	// redeliver it forever) and must not create an alert.
	err := c.Handle(&sarama.ConsumerMessage{Topic: "logs", Value: []byte(`{not valid json`)})
	if err != nil {
		t.Fatalf("malformed message should be swallowed, got error: %v", err)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("malformed message should not create an alert, got %d", len(store.inserted))
	}
}

func TestHandle_InsertErrorIsReturnedForRetry(t *testing.T) {
	store := &fakeStore{failWith: errors.New("db down")}
	c := NewLogConsumer(store, nil)

	// A transient store failure on a real alert must propagate so Kafka retries
	// the message rather than dropping the alert.
	err := c.Handle(msgFor(t, models.LogEntry{ID: "1", ServiceName: "s", Level: models.LogLevelError, Message: "boom"}))
	if err == nil {
		t.Fatal("expected Handle to return the store error so the message is retried")
	}
}
