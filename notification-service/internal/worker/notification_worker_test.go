package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/pulsetrace/shared/models"
)

func sampleEvent() models.NotificationEvent {
	return models.NotificationEvent{
		ID:         "n1",
		IncidentID: "inc-42",
		Title:      "payments-api degraded",
		Body:       "error rate 8%",
		Severity:   models.LogLevelError,
		Services:   []string{"payments-api", "postgres"},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestHandle_MalformedEventIsSwallowed(t *testing.T) {
	w := &NotificationWorker{httpClient: http.DefaultClient}
	// Malformed events must not be returned as an error (which would make
	// RabbitMQ redeliver forever).
	if err := w.Handle(context.Background(), []byte(`{bad json`)); err != nil {
		t.Fatalf("malformed event should be swallowed, got: %v", err)
	}
}

func TestHandle_NoChannelsConfigured_LogsOnlyAndSucceeds(t *testing.T) {
	// With neither Slack nor SMTP configured, dispatch still succeeds via the
	// always-on log channel and makes no network calls.
	w := &NotificationWorker{httpClient: http.DefaultClient}
	if err := w.Handle(context.Background(), mustJSON(t, sampleEvent())); err != nil {
		t.Fatalf("expected success with only the log channel, got: %v", err)
	}
}

func TestHandle_SlackConfigured_PostsPayload(t *testing.T) {
	var mu sync.Mutex
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = string(b)
		mu.Unlock()
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := &NotificationWorker{slackWebhook: srv.URL, httpClient: http.DefaultClient}
	if err := w.Handle(context.Background(), mustJSON(t, sampleEvent())); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotBody == "" {
		t.Fatal("Slack webhook was never called")
	}
	// The delivered message must actually carry the incident details, not an
	// empty stub.
	if !strings.Contains(gotBody, "payments-api degraded") || !strings.Contains(gotBody, "inc-42") {
		t.Fatalf("Slack payload missing incident details: %s", gotBody)
	}
}

func TestHandle_SlackFailureIsReturnedForRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := &NotificationWorker{slackWebhook: srv.URL, httpClient: http.DefaultClient}
	// A failing delivery must surface as an error so the message is retried
	// rather than an incident being silently un-notified.
	if err := w.Handle(context.Background(), mustJSON(t, sampleEvent())); err == nil {
		t.Fatal("expected a dispatch error when the Slack webhook returns 500")
	}
}
