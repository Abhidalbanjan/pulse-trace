package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// openTestDB connects to the database named by DATABASE_URL and isolates the
// test in a throwaway schema (via search_path) so it never touches real tables
// and cleans itself up. Skips the whole test if DATABASE_URL is unset, so the
// suite still passes in environments without Postgres.
func openTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed migrate test")
	}

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(1) // pin one conn so search_path sticks for the test

	schema := fmt.Sprintf("migtest_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		db.Close()
		t.Fatalf("create test schema: %v", err)
	}
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		db.Close()
		t.Fatalf("set search_path: %v", err)
	}

	cleanup := func() {
		_, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
		db.Close()
	}
	return db, cleanup
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	// to_regclass resolves against the connection's search_path, matching where
	// the migrations created the tables.
	if err := db.QueryRow("SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return exists
}

func rowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestRun_AppliesInOrderAndTracks(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	files := fstest.MapFS{
		// Multi-statement file: create + seed in one migration. Exercises the
		// simple-protocol multi-statement path the pgx services rely on.
		"001_widgets.sql": &fstest.MapFile{Data: []byte(
			`CREATE TABLE widgets (id serial PRIMARY KEY, name text);
			 INSERT INTO widgets (name) VALUES ('alpha'), ('beta');`)},
		"002_gadgets.sql": &fstest.MapFile{Data: []byte(
			`CREATE TABLE gadgets (id serial PRIMARY KEY);`)},
		"notes.txt": &fstest.MapFile{Data: []byte("ignored, not a .sql file")},
	}

	if err := Run(ctx, db, "svcA", files); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	if !tableExists(t, db, "widgets") || !tableExists(t, db, "gadgets") {
		t.Fatal("expected both migration tables to exist")
	}
	if n := rowCount(t, db, "widgets"); n != 2 {
		t.Fatalf("expected 2 seeded widgets (multi-statement applied), got %d", n)
	}
	if !tableExists(t, db, "schema_migrations_svcA") {
		t.Fatal("expected per-service tracking table")
	}
	if n := rowCount(t, db, "schema_migrations_svcA"); n != 2 {
		t.Fatalf("expected 2 recorded versions, got %d", n)
	}
}

func TestRun_IsIdempotentAndAppliesOnlyNew(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	files := fstest.MapFS{
		"001_a.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE a (id int);`)},
	}
	if err := Run(ctx, db, "svcB", files); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// A second run over the same set must be a no-op: if it re-executed
	// 001_a.sql the CREATE TABLE (no IF NOT EXISTS) would error.
	if err := Run(ctx, db, "svcB", files); err != nil {
		t.Fatalf("second Run should be a no-op, got: %v", err)
	}

	// Add a new migration; only it should apply.
	files["002_b.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE b (id int);`)}
	if err := Run(ctx, db, "svcB", files); err != nil {
		t.Fatalf("third Run (new migration): %v", err)
	}
	if !tableExists(t, db, "b") {
		t.Fatal("expected newly-added migration to create table b")
	}
	if n := rowCount(t, db, "schema_migrations_svcB"); n != 2 {
		t.Fatalf("expected 2 recorded versions, got %d", n)
	}
}

func TestRun_ServicesAreIsolated(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	files := fstest.MapFS{
		"001_shared_name.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE IF NOT EXISTS thing (id int);`)},
	}
	// Two services sharing the database must each track independently in their
	// own schema_migrations_<service> table - the core reason for per-service
	// tracking. Both should succeed against the same (idempotent) migration.
	if err := Run(ctx, db, "one", files); err != nil {
		t.Fatalf("service one: %v", err)
	}
	if err := Run(ctx, db, "two", files); err != nil {
		t.Fatalf("service two: %v", err)
	}
	if !tableExists(t, db, "schema_migrations_one") || !tableExists(t, db, "schema_migrations_two") {
		t.Fatal("expected separate tracking tables per service")
	}
}

func TestRun_FailingMigrationRollsBack(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	files := fstest.MapFS{
		"001_ok.sql":  &fstest.MapFile{Data: []byte(`CREATE TABLE ok_tbl (id int);`)},
		"002_bad.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE bad_tbl (id int); THIS IS NOT SQL;`)},
	}
	err := Run(ctx, db, "svcC", files)
	if err == nil {
		t.Fatal("expected Run to fail on the bad migration")
	}
	// 001 applied and is recorded; 002 must have rolled back entirely - neither
	// its table nor its version row should survive.
	if !tableExists(t, db, "ok_tbl") {
		t.Fatal("expected the first, valid migration to have applied")
	}
	if tableExists(t, db, "bad_tbl") {
		t.Fatal("failed migration's partial DDL should have rolled back")
	}
	if n := rowCount(t, db, "schema_migrations_svcC"); n != 1 {
		t.Fatalf("expected only the successful version recorded, got %d", n)
	}
}

func TestSanitizeIdent(t *testing.T) {
	cases := map[string]string{
		"gateway":         "gateway",
		"correlation":     "correlation",
		"my-service":      "my_service",
		"drop table x;--": "droptablex__", // spaces/semicolons dropped, each dash -> underscore
		"":                "service",
	}
	for in, want := range cases {
		if got := sanitizeIdent(in); got != want {
			t.Errorf("sanitizeIdent(%q) = %q, want %q", in, got, want)
		}
	}
}
