package worker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

// captureServer returns an httptest server that records the last request's body
// and headers, and replies with the given status.
func captureServer(t *testing.T, status int, gotBody *string, gotHeaders *http.Header) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*gotBody = string(b)
		h := r.Header.Clone()
		*gotHeaders = h
		mu.Unlock()
		rw.WriteHeader(status)
	}))
	return srv
}

func TestHandle_PagerDutyConfigured_PostsTriggerPayload(t *testing.T) {
	var body string
	var headers http.Header
	// PagerDuty's Events API answers 202 Accepted on success.
	srv := captureServer(t, http.StatusAccepted, &body, &headers)
	defer srv.Close()

	w := &NotificationWorker{
		pagerdutyRoutingKey: "rk-123",
		pagerdutyURL:        srv.URL, // point the send at the stub
		httpClient:          http.DefaultClient,
	}
	if err := w.Handle(context.Background(), mustJSON(t, sampleEvent())); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	var got pagerdutyEvent
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("PagerDuty payload was not valid JSON: %v (%s)", err, body)
	}
	if got.RoutingKey != "rk-123" {
		t.Errorf("routing key = %q, want rk-123", got.RoutingKey)
	}
	if got.EventAction != "trigger" {
		t.Errorf("event_action = %q, want trigger", got.EventAction)
	}
	// Dedup on the incident ID so repeated notifications coalesce.
	if got.DedupKey != "inc-42" {
		t.Errorf("dedup_key = %q, want inc-42", got.DedupKey)
	}
	// ERROR must map to PagerDuty's "error" severity.
	if got.Payload.Severity != "error" {
		t.Errorf("severity = %q, want error", got.Payload.Severity)
	}
	// Source should be the first affected service, not a constant.
	if got.Payload.Source != "payments-api" {
		t.Errorf("source = %q, want payments-api", got.Payload.Source)
	}
}

func TestHandle_OpsgenieConfigured_PostsAuthorizedAlert(t *testing.T) {
	var body string
	var headers http.Header
	srv := captureServer(t, http.StatusAccepted, &body, &headers)
	defer srv.Close()

	w := &NotificationWorker{
		opsgenieAPIKey: "gk-abc",
		opsgenieAPIURL: srv.URL,
		httpClient:     http.DefaultClient,
	}
	if err := w.Handle(context.Background(), mustJSON(t, sampleEvent())); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// Opsgenie authenticates via a GenieKey Authorization header.
	if auth := headers.Get("Authorization"); auth != "GenieKey gk-abc" {
		t.Errorf("Authorization = %q, want %q", auth, "GenieKey gk-abc")
	}
	var got opsgenieAlert
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("Opsgenie payload was not valid JSON: %v (%s)", err, body)
	}
	if got.Alias != "inc-42" {
		t.Errorf("alias = %q, want inc-42", got.Alias)
	}
	if got.Priority != "P2" {
		t.Errorf("priority = %q, want P2 for ERROR", got.Priority)
	}
}

func TestHandle_WebhookConfigured_SignsBody(t *testing.T) {
	var body string
	var headers http.Header
	srv := captureServer(t, http.StatusOK, &body, &headers)
	defer srv.Close()

	const secret = "s3cr3t"
	w := &NotificationWorker{webhookURL: srv.URL, webhookSecret: secret, httpClient: http.DefaultClient}
	if err := w.Handle(context.Background(), mustJSON(t, sampleEvent())); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	sig := headers.Get("X-PulseTrace-Signature")
	if sig == "" {
		t.Fatal("expected an X-PulseTrace-Signature header, got none")
	}
	// The signature must be the HMAC-SHA256 of the exact body we received.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != want {
		t.Errorf("signature = %q, want %q", sig, want)
	}
}

func TestHandle_WebhookWithoutSecret_IsUnsigned(t *testing.T) {
	var body string
	var headers http.Header
	srv := captureServer(t, http.StatusNoContent, &body, &headers)
	defer srv.Close()

	w := &NotificationWorker{webhookURL: srv.URL, httpClient: http.DefaultClient}
	if err := w.Handle(context.Background(), mustJSON(t, sampleEvent())); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if sig := headers.Get("X-PulseTrace-Signature"); sig != "" {
		t.Errorf("expected no signature header without a secret, got %q", sig)
	}
}
