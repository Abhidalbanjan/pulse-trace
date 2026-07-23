package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/pulsetrace/shared/migrate"
)

// TestGatewaySchemaProvisionsFromScratch runs this service's real embedded
// migrations against an empty schema and asserts every table gateway-service
// depends on gets created. This is the regression guard for the failure mode
// that motivated the migration runner: a fresh database previously came up
// missing all of these because nothing ran the SQL.
//
// It isolates itself in a throwaway schema so it never touches real data, and
// skips when DATABASE_URL is unset (e.g. CI without a Postgres service).
func TestGatewaySchemaProvisionsFromScratch(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping schema provisioning test")
	}

	db, err := sql.Open("postgres", dsn) // lib/pq: multi-statement Exec works natively
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // pin one conn so search_path sticks

	schema := fmt.Sprintf("gwprov_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	if err := migrate.Run(context.Background(), db, "gateway_test", FS); err != nil {
		t.Fatalf("migrations failed on a fresh schema: %v", err)
	}

	// Every table a gateway handler queries must exist after provisioning.
	wantTables := []string{
		"users", "error_groups", "deployments", "roles", "abac_policies",
		"audit_log", "rate_limit_rules", "alert_rules",
	}
	for _, tbl := range wantTables {
		var exists bool
		if err := db.QueryRow("SELECT to_regclass($1) IS NOT NULL", tbl).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("expected table %q to be created by migrations, it was not", tbl)
		}
	}

	// The seed data the built-in roles/policies depend on must be present too.
	var roleCount int
	if err := db.QueryRow("SELECT count(*) FROM roles WHERE is_system = true").Scan(&roleCount); err != nil {
		t.Fatalf("count system roles: %v", err)
	}
	if roleCount < 3 {
		t.Errorf("expected the 3 built-in roles (admin/editor/viewer) to be seeded, got %d", roleCount)
	}
}
