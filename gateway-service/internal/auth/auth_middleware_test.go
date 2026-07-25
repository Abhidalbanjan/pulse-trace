package auth

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	gatewaymigrations "github.com/pulsetrace/gateway-service/migrations"
	"github.com/pulsetrace/shared/migrate"
)

// captureHandler records the identity headers the downstream handler actually
// receives — i.e. what the middleware decided the request's tenant is.
func captureHandler(seen *http.Header) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	})
}

// TestIngestSpoofedTenantHeaderIsIgnored is the core regression guard for the
// tenant-spoofing fix: a client that sets X-Tenant-ID itself on an ingestion
// request must NOT have that value trusted. With no valid ingestion key it falls
// back to the "default" tenant, never the attacker-chosen one.
func TestIngestSpoofedTenantHeaderIsIgnored(t *testing.T) {
	var seen http.Header
	mw := AuthMiddleware(NewIngestionKeyStore(nil))(captureHandler(&seen))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs", nil)
	req.Header.Set("X-Tenant-ID", "victim-tenant")
	req.Header.Set("X-Tenant-Tier", "premium")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := seen.Get("X-Tenant-ID"); got != defaultTenantID {
		t.Errorf("spoofed tenant leaked through: X-Tenant-ID = %q, want %q", got, defaultTenantID)
	}
	if got := seen.Get("X-Tenant-Tier"); got != defaultTenantTier {
		t.Errorf("spoofed tier leaked through: X-Tenant-Tier = %q, want %q", got, defaultTenantTier)
	}
}

// TestProtectedRouteStripsSpoofedIdentityHeaders asserts a caller can't forge the
// X-User-Role / X-Tenant-ID headers downstream services trust: without a valid
// JWT the request is rejected, and the forged headers never reach the handler.
func TestProtectedRouteStripsSpoofedIdentityHeaders(t *testing.T) {
	var seen http.Header
	mw := AuthMiddleware(NewIngestionKeyStore(nil))(captureHandler(&seen))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil)
	req.Header.Set("X-User-Role", "admin")
	req.Header.Set("X-Tenant-ID", "victim-tenant")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for forged-header request with no JWT, got %d", rr.Code)
	}
	if seen != nil {
		t.Fatalf("handler should never have been reached for an unauthorized request")
	}
}

// TestServerIngestRequiresKeyWhenEnforced asserts REQUIRE_INGESTION_KEY makes an
// un-keyed server-side ingestion request fail closed (401) instead of silently
// landing in the default tenant.
func TestServerIngestRequiresKeyWhenEnforced(t *testing.T) {
	orig := requireIngestionKey
	requireIngestionKey = true
	defer func() { requireIngestionKey = orig }()

	mw := AuthMiddleware(NewIngestionKeyStore(nil))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when REQUIRE_INGESTION_KEY is set and no key present, got %d", rr.Code)
	}
}

// TestIngestionKeyCrossTenantIsolation is a DB-backed check that a key only ever
// resolves to its OWN tenant, that a revoked key stops resolving, and that an
// unknown key resolves to nothing. Skips when DATABASE_URL is unset.
func TestIngestionKeyCrossTenantIsolation(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed ingestion key test")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // pin one conn so search_path sticks

	schema := fmt.Sprintf("ingestkey_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if err := migrate.Run(context.Background(), db, "ingestkey_test", gatewaymigrations.FS); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}

	store := NewIngestionKeyStore(db)
	ctx := context.Background()

	// Two tenants, each with their own key.
	keyA := seedKey(t, db, "tenant-a", "standard")
	keyB := seedKey(t, db, "tenant-b", "premium")

	if tid, tier, scope, ok := store.Resolve(ctx, keyA); !ok || tid != "tenant-a" || tier != "standard" || scope != ScopeIngest {
		t.Errorf("key A resolved to (%q,%q,%q,%v), want (tenant-a,standard,ingest,true)", tid, tier, scope, ok)
	}
	if tid, tier, _, ok := store.Resolve(ctx, keyB); !ok || tid != "tenant-b" || tier != "premium" {
		t.Errorf("key B resolved to (%q,%q,%v), want (tenant-b,premium,true)", tid, tier, ok)
	}
	// Neither key ever resolves to the other's tenant — implicit in the above,
	// but assert an unknown key yields nothing at all.
	if _, _, _, ok := store.Resolve(ctx, "pt_ingest_totally-made-up"); ok {
		t.Errorf("an unknown key must not resolve to any tenant")
	}

	// A public RUM-scoped key resolves with its scope so callers can enforce it.
	keyR := seedKeyScoped(t, db, "tenant-a", "standard", ScopeRUM)
	if _, _, scope, ok := store.Resolve(ctx, keyR); !ok || scope != ScopeRUM {
		t.Errorf("rum key resolved to scope %q (ok=%v), want rum/true", scope, ok)
	}

	// Revoke A; it must stop resolving (cache is invalidated on revoke).
	if _, err := db.Exec("UPDATE ingestion_keys SET revoked_at = now() WHERE key_hash = $1", hashIngestionKey(keyA)); err != nil {
		t.Fatalf("revoke key A: %v", err)
	}
	store.invalidateCache()
	if _, _, _, ok := store.Resolve(ctx, keyA); ok {
		t.Errorf("a revoked key must not resolve")
	}
}

// seedKey inserts an ingest-scoped key for the given tenant and returns the plaintext.
func seedKey(t *testing.T, db *sql.DB, tenantID, tier string) string {
	return seedKeyScoped(t, db, tenantID, tier, ScopeIngest)
}

// seedKeyScoped inserts a key of the given scope and returns the plaintext.
func seedKeyScoped(t *testing.T, db *sql.DB, tenantID, tier, scope string) string {
	t.Helper()
	plaintext, prefix, hash, err := generateIngestionKey(scope)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO ingestion_keys (name, key_prefix, key_hash, tenant_id, tier, scope) VALUES ($1,$2,$3,$4,$5,$6)",
		"test-"+tenantID+"-"+scope, prefix, hash, tenantID, tier, scope,
	); err != nil {
		t.Fatalf("insert key for %s: %v", tenantID, err)
	}
	return plaintext
}
