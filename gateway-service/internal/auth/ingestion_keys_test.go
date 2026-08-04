package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	gatewaymigrations "github.com/pulsetrace/gateway-service/migrations"
	"github.com/pulsetrace/shared/migrate"
)

// setupIngestKeyDB provisions an isolated schema with the gateway migrations
// applied, or skips when DATABASE_URL is unset (same contract as the other
// DB-backed auth tests). Returns a store bound to the DB.
func setupIngestKeyDB(t *testing.T) (*IngestionKeyStore, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed ingestion key test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // pin one conn so search_path sticks

	schema := fmt.Sprintf("ingestrot_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE") })
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if err := migrate.Run(context.Background(), db, "ingestrot_test", gatewaymigrations.FS); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return NewIngestionKeyStore(db), db
}

type mintedKeyResp struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Scope            string `json:"scope"`
	Key              string `json:"key"`
	RotatedFrom      string `json:"rotated_from"`
	GracePeriod      string `json:"grace_period"`
	OldKeyValidUntil string `json:"old_key_valid_until"`
}

func createKey(t *testing.T, store *IngestionKeyStore, name, scope string) mintedKeyResp {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"scope":%q}`, name, scope)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/ingestion-keys", strings.NewReader(body))
	rr := httptest.NewRecorder()
	store.CreateIngestionKey(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create key: status %d, body %s", rr.Code, rr.Body.String())
	}
	var out mintedKeyResp
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode create resp: %v", err)
	}
	return out
}

// rotateKey invokes the rotate handler for id with the given grace period and
// returns the recorder for status/body assertions plus the decoded response.
func rotateKey(t *testing.T, store *IngestionKeyStore, id, grace string) (*httptest.ResponseRecorder, mintedKeyResp) {
	t.Helper()
	body := "{}"
	if grace != "" {
		body = fmt.Sprintf(`{"grace_period":%q}`, grace)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/ingestion-keys/"+id+"/rotate", strings.NewReader(body))
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	store.RotateIngestionKey(rr, req)
	var out mintedKeyResp
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

// TestRotateIngestionKeyGraceLifecycle is the F4 rotation contract: after a
// rotation with a non-zero grace window BOTH the old and new keys resolve (no
// downtime while agents pick up the replacement), the old key is linked to its
// successor and scheduled for future revocation; a grace-0 rotation kills the
// predecessor immediately. Skips when DATABASE_URL is unset.
func TestRotateIngestionKeyGraceLifecycle(t *testing.T) {
	store, db := setupIngestKeyDB(t)
	ctx := context.Background()

	orig := createKey(t, store, "prod-agents", ScopeIngest)
	if _, _, _, ok := store.Resolve(ctx, orig.Key); !ok {
		t.Fatalf("freshly created key must resolve")
	}

	// Rotate with a 24h grace window.
	rr, rotated := rotateKey(t, store, orig.ID, "24h")
	if rr.Code != http.StatusCreated {
		t.Fatalf("rotate: status %d, body %s", rr.Code, rr.Body.String())
	}
	if rotated.RotatedFrom != orig.ID || rotated.ID == orig.ID || rotated.Key == orig.Key {
		t.Fatalf("rotation should mint a distinct successor linked to the original: %+v", rotated)
	}

	// Both keys resolve during the grace window — this is the whole point.
	if _, _, _, ok := store.Resolve(ctx, orig.Key); !ok {
		t.Errorf("old key must stay valid during the grace window")
	}
	if _, _, _, ok := store.Resolve(ctx, rotated.Key); !ok {
		t.Errorf("new key must be valid immediately after rotation")
	}

	// The old row is linked to the successor and scheduled for FUTURE revocation.
	var replacedBy sql.NullString
	var revokedAt sql.NullTime
	if err := db.QueryRow(
		"SELECT replaced_by, revoked_at FROM ingestion_keys WHERE id = $1", orig.ID,
	).Scan(&replacedBy, &revokedAt); err != nil {
		t.Fatalf("load rotated-out key: %v", err)
	}
	if !replacedBy.Valid || replacedBy.String != rotated.ID {
		t.Errorf("old key replaced_by = %v, want %s", replacedBy, rotated.ID)
	}
	if !revokedAt.Valid || !revokedAt.Time.After(time.Now()) {
		t.Errorf("old key revoked_at = %v, want a future timestamp (grace window)", revokedAt)
	}

	// Grace-0 rotation of the successor revokes it immediately (no window).
	rr0, rotated0 := rotateKey(t, store, rotated.ID, "0")
	if rr0.Code != http.StatusCreated {
		t.Fatalf("grace-0 rotate: status %d, body %s", rr0.Code, rr0.Body.String())
	}
	if _, _, _, ok := store.Resolve(ctx, rotated.Key); ok {
		t.Errorf("a grace-0 rotation must kill the predecessor immediately")
	}
	if _, _, _, ok := store.Resolve(ctx, rotated0.Key); !ok {
		t.Errorf("the newest key must resolve after a grace-0 rotation")
	}
}

// TestRotateIngestionKeyValidation covers the guardrails: an over-long or
// malformed grace period is rejected, and rotating an unknown key is a 404.
func TestRotateIngestionKeyValidation(t *testing.T) {
	store, _ := setupIngestKeyDB(t)
	orig := createKey(t, store, "validate-me", ScopeIngest)

	if rr, _ := rotateKey(t, store, orig.ID, "9999h"); rr.Code != http.StatusBadRequest {
		t.Errorf("grace period beyond the 30d max should be 400, got %d", rr.Code)
	}
	if rr, _ := rotateKey(t, store, orig.ID, "not-a-duration"); rr.Code != http.StatusBadRequest {
		t.Errorf("a malformed grace period should be 400, got %d", rr.Code)
	}
	if rr, _ := rotateKey(t, store, "00000000-0000-0000-0000-000000000000", "24h"); rr.Code != http.StatusNotFound {
		t.Errorf("rotating an unknown key should be 404, got %d", rr.Code)
	}
}
