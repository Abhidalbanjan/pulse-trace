package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/pulsetrace/shared/models"
)

// createTableDDL mirrors gateway migration 018 (the table's owning migration);
// notification-service can't import the gateway module, so the DB-backed test
// provisions the table directly.
const createTableDDL = `CREATE TABLE IF NOT EXISTS notification_channels (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(50) NOT NULL DEFAULT 'default',
    name VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
)`

// TestMain configures an encryption key so the whole package can encrypt/decrypt
// (loadAEAD caches via sync.Once, so it must be set before the first use).
func TestMain(m *testing.M) {
	os.Setenv("CHANNEL_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef") // 32 bytes
	os.Exit(m.Run())
}

func TestEncryptRoundTrip(t *testing.T) {
	if !EncryptionConfigured() {
		t.Fatal("encryption should be configured in tests")
	}
	secret := "https://hooks.slack.com/services/T00/B00/xxxx"
	enc, err := Encrypt(secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == secret {
		t.Fatal("ciphertext must differ from plaintext")
	}
	dec, err := Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != secret {
		t.Fatalf("round-trip mismatch: got %q", dec)
	}
	// Two encryptions of the same plaintext differ (random nonce).
	enc2, _ := Encrypt(secret)
	if enc == enc2 {
		t.Error("expected a fresh nonce per encryption")
	}
}

func TestRedactedHidesSecrets(t *testing.T) {
	ch := Channel{Type: TypeSlack, Config: map[string]string{"webhook_url": "https://secret"}}
	r := ch.Redacted()
	if _, ok := r.Config["webhook_url"]; ok {
		t.Error("redacted channel must not expose the secret value")
	}
	if r.Config["webhook_url_set"] != "true" {
		t.Error("redacted channel should flag that a secret is configured")
	}
}

func TestMissingRequired(t *testing.T) {
	if m := MissingRequired(TypeSlack, map[string]string{}); len(m) != 1 || m[0] != "webhook_url" {
		t.Errorf("slack without webhook_url should be missing it, got %v", m)
	}
	if m := MissingRequired(TypeEmail, map[string]string{"host": "h", "to": "a@b"}); len(m) != 0 {
		t.Errorf("email with host+to should be complete, got %v", m)
	}
}

func TestDeliverSlackAndWebhookHMAC(t *testing.T) {
	// Slack: expects a JSON body with a "text" field, answers 200.
	var slackHit bool
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["text"] == "" {
			t.Error("slack payload missing text")
		}
		slackHit = true
	}))
	defer slack.Close()

	event := &models.NotificationEvent{ID: "n1", IncidentID: "i1", Title: "DB down", Body: "boom", Severity: models.LogLevelError, Services: []string{"db"}, CreatedAt: time.Now()}
	if err := Deliver(context.Background(), slack.Client(), Channel{Type: TypeSlack, Enabled: true, Config: map[string]string{"webhook_url": slack.URL}}, event); err != nil {
		t.Fatalf("slack deliver: %v", err)
	}
	if !slackHit {
		t.Error("slack endpoint was not called")
	}

	// Webhook: verify the HMAC signature over the exact body.
	const secret = "shh"
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if got := r.Header.Get("X-PulseTrace-Signature"); got != want {
			t.Errorf("webhook signature mismatch: got %q want %q", got, want)
		}
	}))
	defer webhook.Close()
	if err := Deliver(context.Background(), webhook.Client(), Channel{Type: TypeWebhook, Enabled: true, Config: map[string]string{"url": webhook.URL, "secret": secret}}, event); err != nil {
		t.Fatalf("webhook deliver: %v", err)
	}
}

func TestDeliverDisabledIsNoop(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	event := &models.NotificationEvent{Title: "x", Severity: models.LogLevelInfo}
	if err := Deliver(context.Background(), srv.Client(), Channel{Type: TypeSlack, Enabled: false, Config: map[string]string{"webhook_url": srv.URL}}, event); err != nil {
		t.Fatalf("disabled deliver should be a no-op, got %v", err)
	}
	if called {
		t.Error("a disabled channel must not deliver")
	}
}

func setupChannelsDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed channels test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("chan_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE") })
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		t.Fatalf("search_path: %v", err)
	}
	if _, err := db.Exec(createTableDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// TestRepositoryEncryptsAndRedacts: secrets are ciphertext at rest, decrypted for
// delivery, redacted for the API, and preserved on a blank-secret update.
func TestRepositoryEncryptsAndRedacts(t *testing.T) {
	db := setupChannelsDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	ch, err := repo.Create(ctx, &Channel{TenantID: "acme", Name: "oncall", Type: TypeSlack, Enabled: true, Config: map[string]string{"webhook_url": "https://secret-url"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// At rest: the stored config value must not be the plaintext.
	var raw []byte
	if err := db.QueryRow("SELECT config FROM notification_channels WHERE id=$1", ch.ID).Scan(&raw); err != nil {
		t.Fatalf("read raw: %v", err)
	}
	var stored map[string]string
	_ = json.Unmarshal(raw, &stored)
	if stored["webhook_url"] == "https://secret-url" || stored["webhook_url"] == "" {
		t.Errorf("secret must be stored encrypted, got %q", stored["webhook_url"])
	}

	// Delivery view: decrypted.
	dec, err := repo.ListDecrypted(ctx, "acme")
	if err != nil || len(dec) != 1 || dec[0].Config["webhook_url"] != "https://secret-url" {
		t.Fatalf("ListDecrypted should return plaintext secret, got %+v (err %v)", dec, err)
	}

	// API view: redacted.
	api, _ := repo.ListForAPI(ctx, "acme")
	if _, leaked := api[0].Config["webhook_url"]; leaked {
		t.Error("ListForAPI must not expose the secret")
	}
	if api[0].Config["webhook_url_set"] != "true" {
		t.Error("ListForAPI should flag the secret as configured")
	}

	// Blank-secret update keeps the stored secret.
	if _, err := repo.Update(ctx, "acme", ch.ID, "oncall-renamed", true, map[string]string{"webhook_url": ""}); err != nil {
		t.Fatalf("update: %v", err)
	}
	dec2, _ := repo.ListDecrypted(ctx, "acme")
	if dec2[0].Config["webhook_url"] != "https://secret-url" || dec2[0].Name != "oncall-renamed" {
		t.Errorf("blank-secret update must keep the secret and apply the rename, got %+v", dec2[0])
	}

	// Cross-tenant isolation: another tenant sees nothing.
	if other, _ := repo.ListForAPI(ctx, "other"); len(other) != 0 {
		t.Error("channels must be tenant-scoped")
	}

	// Delete.
	if err := repo.Delete(ctx, "acme", ch.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
