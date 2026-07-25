package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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

// newMigratedSchema spins up a throwaway schema with the real migrations applied,
// returning the db (pinned to one conn so search_path sticks) and a cleanup.
func newMigratedSchema(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("tenants_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DROP SCHEMA " + schema + " CASCADE")
		db.Close()
	})
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if err := migrate.Run(context.Background(), db, "tenants_test", gatewaymigrations.FS); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return db
}

func doSignup(t *testing.T, store *TenantStore, org, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(SignupRequest{OrgName: org, Username: user, Password: pass})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/signup", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	store.Signup(rr, req)
	return rr
}

func TestSignupCreatesTenantAndAdmin(t *testing.T) {
	db := newMigratedSchema(t)
	store := NewTenantStore(db)

	rr := doSignup(t, store, "Acme Inc", "acme-admin", "hunter2")
	if rr.Code != http.StatusCreated {
		t.Fatalf("signup returned %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["token"] == "" || resp["role"] != "admin" {
		t.Fatalf("signup response missing token/admin role: %v", resp)
	}
	tenantID := resp["tenant_id"]
	if tenantID != "acme-inc" {
		t.Errorf("expected slug 'acme-inc', got %q", tenantID)
	}

	// Tenant row exists on the free plan, active.
	var plan, status string
	if err := db.QueryRow("SELECT plan, status FROM tenants WHERE id = $1", tenantID).Scan(&plan, &status); err != nil {
		t.Fatalf("tenant row not found: %v", err)
	}
	if plan != "free" || status != "active" {
		t.Errorf("tenant plan/status = %q/%q, want free/active", plan, status)
	}

	// The registrant is that tenant's ADMIN (not a viewer).
	var role, tid string
	if err := db.QueryRow("SELECT role, tenant_id FROM users WHERE username = $1", "acme-admin").Scan(&role, &tid); err != nil {
		t.Fatalf("admin user not found: %v", err)
	}
	if role != "admin" || tid != tenantID {
		t.Errorf("admin user role/tenant = %q/%q, want admin/%s", role, tid, tenantID)
	}
}

func TestSignupDuplicateUsernameRejected(t *testing.T) {
	db := newMigratedSchema(t)
	store := NewTenantStore(db)

	if rr := doSignup(t, store, "Org One", "dup-user", "pw"); rr.Code != http.StatusCreated {
		t.Fatalf("first signup failed: %d %s", rr.Code, rr.Body.String())
	}
	if rr := doSignup(t, store, "Org Two", "dup-user", "pw"); rr.Code != http.StatusConflict {
		t.Errorf("duplicate username should 409, got %d", rr.Code)
	}
}

func TestSignupSameOrgNameGetsDistinctTenant(t *testing.T) {
	db := newMigratedSchema(t)
	store := NewTenantStore(db)

	rr1 := doSignup(t, store, "Collision Co", "user-1", "pw")
	rr2 := doSignup(t, store, "Collision Co", "user-2", "pw")
	if rr1.Code != http.StatusCreated || rr2.Code != http.StatusCreated {
		t.Fatalf("signups failed: %d / %d", rr1.Code, rr2.Code)
	}
	var t1, t2 map[string]string
	_ = json.Unmarshal(rr1.Body.Bytes(), &t1)
	_ = json.Unmarshal(rr2.Body.Bytes(), &t2)
	if t1["tenant_id"] == t2["tenant_id"] {
		t.Errorf("two orgs with the same name must get distinct tenant ids, both got %q", t1["tenant_id"])
	}
}
