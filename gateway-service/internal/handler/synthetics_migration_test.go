package handler

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// The repair in initPostgresTable only does anything on a database that predates
// multi-tenancy — one whose synthetic_targets carries a single-column
// UNIQUE(url). CI always starts from an empty database and therefore only ever
// exercises the fresh-install path, so the bug this repair fixes (every check
// creation failing with 42P10, "no unique or exclusion constraint matching the
// ON CONFLICT specification") was invisible to it and could regress freely.
//
// These tests manufacture the legacy shape explicitly so the upgrade path is
// covered by CI rather than by whoever happens to have an old volume.

// legacySchema is synthetic_targets exactly as deployments created it before
// tenant_id existed: no tenant column, and uniqueness on url alone.
const legacySchema = `
	CREATE TABLE synthetic_targets (
		id SERIAL PRIMARY KEY,
		url VARCHAR(255) NOT NULL UNIQUE,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`

func newSyntheticsTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed synthetics migration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// One connection so every statement lands in the same session, and therefore
	// sees the search_path we set below.
	db.SetMaxOpenConns(1)

	schema := fmt.Sprintf("synth_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		db.Close()
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		db.Close()
		t.Fatalf("set search_path: %v", err)
	}
	return db, func() {
		_, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
		db.Close()
	}
}

// uniqueConstraints returns the constraint names on synthetic_targets, so the
// assertions describe intent ("the composite one exists") rather than poking at
// pg_constraint inline.
func uniqueConstraints(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`
		SELECT c.conname, pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE t.relname = 'synthetic_targets'
		  AND c.contype = 'u'
		  AND n.nspname = current_schema()`)
	if err != nil {
		t.Fatalf("query constraints: %v", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan constraint: %v", err)
		}
		out[name] = def
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate constraints: %v", err)
	}
	return out
}

// upsert is the exact statement CreateTarget issues. If the constraint is wrong
// this fails with 42P10 — which is precisely how the bug reached production.
func upsert(db *sql.DB, tenant, url, name string) error {
	_, err := db.Exec(
		`INSERT INTO synthetic_targets (tenant_id, url, name, spec) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, url) DO UPDATE SET name = EXCLUDED.name, spec = EXCLUDED.spec`,
		tenant, url, name, nil,
	)
	return err
}

func TestInitPostgresTable_RepairsLegacySingleColumnUnique(t *testing.T) {
	db, cleanup := newSyntheticsTestDB(t)
	defer cleanup()

	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}

	// Sanity: the legacy shape really is broken for the upsert the handler uses.
	// Without this the test could pass for the wrong reason (e.g. if the schema
	// constant drifted and already had the composite key).
	if err := upsert(db, "default", "https://example.com/health", "before"); err == nil {
		t.Fatal("expected the legacy UNIQUE(url) schema to reject ON CONFLICT (tenant_id, url), but the upsert succeeded — the fixture no longer reproduces the bug")
	}

	h := &SyntheticsHandler{DB: db}
	h.initPostgresTable()

	cons := uniqueConstraints(t, db)
	if _, ok := cons["synthetic_targets_url_key"]; ok {
		t.Error("legacy single-column UNIQUE(url) should have been dropped: it prevents two tenants monitoring the same URL, and leaks that someone already does")
	}
	if def, ok := cons["synthetic_targets_tenant_id_url_key"]; !ok {
		t.Errorf("composite UNIQUE(tenant_id, url) missing after repair; constraints present: %v", cons)
	} else if def != "UNIQUE (tenant_id, url)" {
		t.Errorf("unexpected constraint definition: %q", def)
	}

	if err := upsert(db, "default", "https://example.com/health", "after"); err != nil {
		t.Fatalf("upsert should succeed once the composite constraint exists: %v", err)
	}
}

func TestInitPostgresTable_TwoTenantsMaySharePublicURL(t *testing.T) {
	db, cleanup := newSyntheticsTestDB(t)
	defer cleanup()

	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	h := &SyntheticsHandler{DB: db}
	h.initPostgresTable()

	// The tenancy half of the bug: under UNIQUE(url) the second tenant's insert
	// collided with the first's row, so monitoring a public endpoint someone else
	// already watched was impossible — and the failure disclosed that they did.
	if err := upsert(db, "tenant-a", "https://status.example.com", "a"); err != nil {
		t.Fatalf("tenant-a insert: %v", err)
	}
	if err := upsert(db, "tenant-b", "https://status.example.com", "b"); err != nil {
		t.Fatalf("tenant-b must be able to monitor the same public URL as tenant-a: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM synthetic_targets WHERE url = 'https://status.example.com'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("expected one row per tenant, got %d", n)
	}
}

func TestInitPostgresTable_IsIdempotent(t *testing.T) {
	db, cleanup := newSyntheticsTestDB(t)
	defer cleanup()

	// From scratch, then repeatedly: init runs on every boot, so a second pass
	// must not fail or duplicate constraints.
	h := &SyntheticsHandler{DB: db}
	h.initPostgresTable()
	h.initPostgresTable()

	cons := uniqueConstraints(t, db)
	if len(cons) != 1 {
		t.Errorf("expected exactly one unique constraint after repeated init, got %v", cons)
	}
	if _, ok := cons["synthetic_targets_tenant_id_url_key"]; !ok {
		t.Errorf("composite constraint missing on a fresh install: %v", cons)
	}
	if err := upsert(db, "default", "https://example.com/fresh", "x"); err != nil {
		t.Fatalf("upsert on a fresh install: %v", err)
	}
}
