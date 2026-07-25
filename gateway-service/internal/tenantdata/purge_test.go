package tenantdata

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	gatewaymigrations "github.com/pulsetrace/gateway-service/migrations"
	"github.com/pulsetrace/shared/migrate"
)

func TestChQuote(t *testing.T) {
	if got := chQuote("acme"); got != "'acme'" {
		t.Errorf("chQuote(acme) = %q", got)
	}
	// single quotes are doubled to prevent breaking out of the literal
	if got := chQuote("a'b"); got != "'a''b'" {
		t.Errorf("chQuote(a'b) = %q, want 'a''b'", got)
	}
}

// TestPurgePostgresFull is a DB-backed check that a full purge removes every one
// of a tenant's Postgres rows — including the tenant itself — and leaves other
// tenants untouched. Skips without DATABASE_URL.
func TestPurgePostgresFull(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed purge test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("purge_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		t.Fatalf("search_path: %v", err)
	}
	if err := migrate.Run(context.Background(), db, "purge_test", gatewaymigrations.FS); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	// Two tenants; only "victim" gets purged.
	for _, tid := range []string{"victim", "keeper"} {
		mustExec(t, db, "INSERT INTO tenants (id, name) VALUES ($1,$1)", tid)
		mustExec(t, db, "INSERT INTO users (username, password_hash, role, tenant_id) VALUES ($1,'x','admin',$2)", tid+"-admin", tid)
		mustExec(t, db, "INSERT INTO deployments (tenant_id, service, version) VALUES ($1,'svc','v1')", tid)
		mustExec(t, db, "INSERT INTO usage_daily (tenant_id, day, signal, count) VALUES ($1, CURRENT_DATE, 'traces', 5)", tid)
	}

	p := &Purger{db: db} // db-only purger; external stores are no-ops/skipped here
	res := p.PurgeTenant(context.Background(), "victim", true)
	if len(res.Steps) == 0 {
		t.Fatalf("expected some purge steps to succeed, got errors: %v", res.Errors)
	}

	// victim is gone everywhere...
	for _, table := range []string{"tenants", "users", "deployments", "usage_daily"} {
		var n int
		col := "tenant_id"
		id := "victim"
		if table == "tenants" {
			col = "id"
		}
		if err := db.QueryRow("SELECT count(*) FROM "+table+" WHERE "+col+" = $1", id).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("victim rows remain in %s: %d", table, n)
		}
	}
	// ...but keeper is untouched.
	var keeperUsers int
	db.QueryRow("SELECT count(*) FROM users WHERE tenant_id = 'keeper'").Scan(&keeperUsers)
	if keeperUsers != 1 {
		t.Errorf("keeper's data was affected: users=%d, want 1", keeperUsers)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
