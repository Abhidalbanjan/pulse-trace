package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "github.com/pulsetrace/shared/db/driver/postgres"
	_ "github.com/pulsetrace/shared/db/driver/sqlite"
)

// The golden schema test.
//
// # What it is for
//
// The dialect layer claims the same migrations produce the same schema on both
// backends. That claim is worth exactly as much as the evidence for it, and the
// evidence cannot be a reading of the rewrite rules — they handle the
// constructs I noticed, and thirty-four migrations written against Postgres
// over months contain constructs nobody notices until they fail.
//
// So this applies every migration to SQLite and, when a Postgres is available,
// to Postgres too, then compares the tables and columns that came out.
//
// # What building it found
//
// Running it the first time, 19 of 26 gateway migrations failed. Almost all of
// it was one bug of mine — the statement splitter treated a `;` inside a `--`
// comment as a terminator, cutting statements mid-sentence and producing errors
// like `near "this": syntax error` from prose. The rest were four real
// incompatibilities: `::jsonb` casts, `ADD COLUMN IF NOT EXISTS`,
// `INSERT … SELECT … ON CONFLICT` needing an intervening WHERE, and multi-column
// ALTER, which is a difference in statement *count* and so cannot be expressed
// as a rewrite at all.
//
// Exactly one migration proved untranslatable and got a hand-written sibling:
// 015 uses the jsonb containment operator `@>`, which has no SQLite operator
// form. That is the escape hatch working as intended rather than the rules
// being widened until they misfire on something they half understand.

// migrationDirs are applied in this order because in lite mode every service
// shares one database, and correlation's 004 alters `alerts` — a table owned by
// alert-service. In the cluster that ordering is incidental; in a single binary
// it is a dependency, and this is where it gets pinned.
//
// log-service is in this list because leaving it out is what let the
// type-position bug ship. Its schema has a column *named* `timestamp`, and it
// is the only directory that does — so the one file capable of disproving the
// rewrite rules was the one file this test did not read. A golden test whose
// premise is "the evidence cannot be a reading of the rules" has to read
// everything.
var migrationDirs = []string{"gateway-service", "log-service", "alert-service", "correlation-service"}

// applyMigrations runs every migration for every service against db.
func applyMigrations(t *testing.T, conn *sql.DB, d Dialect) {
	t.Helper()
	for _, svc := range migrationDirs {
		dir := filepath.Join("..", "..", svc, "migrations")
		for _, name := range migrationFiles(t, dir) {
			path := filepath.Join(dir, name)
			if d.Kind() == SQLite {
				if sib := strings.TrimSuffix(path, ".sql") + ".sqlite.sql"; fileExists(sib) {
					path = sib
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, stmt := range ExpandStatements(string(raw)) {
				if strings.TrimSpace(stmt) == "" {
					continue
				}
				if err := ExecStatement(context.Background(), conn, d, stmt); err != nil {
					t.Fatalf("%s/%s on %s: %v\n  statement: %.160s",
						svc, name, d.Kind(), err, strings.ReplaceAll(stmt, "\n", " "))
				}
			}
		}
	}
}

// migrationFiles lists a service's migrations in application order, skipping
// SQLite siblings — those are reached through their Postgres original.
func migrationFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if strings.HasSuffix(e.Name(), ".sqlite.sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Every migration must apply cleanly to SQLite. A failure here names the file
// and the statement, because "migrations failed" is not actionable at 3am.
func TestAllMigrationsApplyToSQLite(t *testing.T) {
	conn, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "lite.db")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	d := sqliteDialect{}
	if err := d.EnsureChainLock(context.Background(), conn); err != nil {
		t.Fatalf("ensure chain lock: %v", err)
	}
	applyMigrations(t, conn, d)

	tables := sqliteTables(t, conn)
	if len(tables) < 25 {
		t.Fatalf("only %d tables after migrating; the schema is not complete", len(tables))
	}
	t.Logf("%d tables created on SQLite", len(tables))
}

// Re-applying every migration must be a no-op. The runner records applied
// versions, but the files are written idempotently on purpose and the
// rewrite strips the `IF NOT EXISTS` that made ADD COLUMN idempotent — so this
// is the test that catches ExecStatement failing to put that back.
func TestMigrationsAreIdempotentOnSQLite(t *testing.T) {
	conn, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "lite.db")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	d := sqliteDialect{}
	if err := d.EnsureChainLock(context.Background(), conn); err != nil {
		t.Fatalf("ensure chain lock: %v", err)
	}
	applyMigrations(t, conn, d)
	first := sqliteTables(t, conn)

	applyMigrations(t, conn, d) // again
	second := sqliteTables(t, conn)

	if len(first) != len(second) {
		t.Errorf("re-applying changed the table count: %d then %d", len(first), len(second))
	}
}

// The golden comparison: the same migrations must produce the same tables and
// columns on both backends.
func TestSchemaMatchesPostgres(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to diff the SQLite schema against Postgres")
	}

	lite, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "lite.db")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer lite.Close()
	sd := sqliteDialect{}
	if err := sd.EnsureChainLock(context.Background(), lite); err != nil {
		t.Fatalf("chain lock: %v", err)
	}
	applyMigrations(t, lite, sd)

	// DATABASE_URL set but Postgres unreachable is a failure, not a skip.
	//
	// Skipping there produces a green tick for a comparison that never
	// happened — the same shape as the Kafka half of the bus conformance suite
	// silently not running. If the environment says a database is available,
	// this test's job is to use it or say loudly that it could not. Absence of
	// the variable is still a skip, which is the developer-machine case.
	pg, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("DATABASE_URL is set but the driver rejected it: %v", err)
	}
	defer pg.Close()
	if err := pg.Ping(); err != nil {
		t.Fatalf("DATABASE_URL is set but Postgres is unreachable: %v", err)
	}
	// One connection, because `SET search_path` is connection-scoped.
	//
	// Issued on a pool it binds to whichever connection served that Exec; the
	// thirty-odd DDL statements that follow may land on a different one, which
	// starts at the default search_path. Those tables are then created in
	// `public` of whatever DATABASE_URL points at — a test writing into a real
	// database — and `DROP SCHEMA … CASCADE` does not clean them up. The
	// column-count guard below only notices afterwards.
	pg.SetMaxOpenConns(1)

	// A throwaway schema so this never touches a real database's tables.
	schema := "golden_schema_probe"
	if _, err := pg.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if _, err := pg.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
	if _, err := pg.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatalf("search_path: %v", err)
	}
	// Confirm it took on the connection the migrations will actually use,
	// rather than trusting that the pool handed back the same one.
	var active string
	if err := pg.QueryRow(`SHOW search_path`).Scan(&active); err != nil {
		t.Fatalf("read search_path: %v", err)
	}
	if !strings.Contains(active, schema) {
		t.Fatalf("search_path is %q, not %q — the migrations would be written to "+
			"the default schema of a real database", active, schema)
	}
	applyMigrations(t, pg, postgresDialect{})

	liteCols := sqliteColumns(t, lite)
	pgCols := postgresColumns(t, pg, schema)

	// Neither side may be empty. A comparison of nothing against nothing
	// reports zero differences and passes — which is how a broken
	// information_schema query, or a search_path that silently put the tables
	// somewhere else, would look exactly like success. Several tests in this
	// codebase have gone green that way; this one is not going to.
	if len(pgCols) < 100 {
		t.Fatalf("only %d Postgres columns found — the migrations did not land in schema %q, "+
			"so this comparison would pass without comparing anything", len(pgCols), schema)
	}
	if len(liteCols) < 100 {
		t.Fatalf("only %d SQLite columns found — nothing to compare against", len(liteCols))
	}

	var missing, extra []string
	for k := range pgCols {
		if !liteCols[k] {
			missing = append(missing, k)
		}
	}
	for k := range liteCols {
		if !pgCols[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	// Objects the dialect layer creates for itself are expected to differ.
	filtered := extra[:0]
	for _, e := range extra {
		if strings.HasPrefix(e, "chain_lock.") || strings.HasPrefix(e, "sqlite_") {
			continue
		}
		filtered = append(filtered, e)
	}
	extra = filtered

	if len(missing) > 0 {
		t.Errorf("%d columns exist on Postgres but not SQLite:\n  %s",
			len(missing), strings.Join(truncate(missing, 25), "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("%d columns exist on SQLite but not Postgres:\n  %s",
			len(extra), strings.Join(truncate(extra, 25), "\n  "))
	}
	t.Logf("compared %d Postgres columns against %d SQLite columns", len(pgCols), len(liteCols))
}

func truncate(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(s[:n:n], fmt.Sprintf("… and %d more", len(s)-n))
}

func sqliteTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'
		AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, n)
	}
	return out
}

// sqliteColumns returns a set of "table.column".
func sqliteColumns(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, tbl := range sqliteTables(t, db) {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, tbl)
		if err != nil {
			t.Fatalf("pragma %s: %v", tbl, err)
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				t.Fatalf("scan: %v", err)
			}
			out[strings.ToLower(tbl+"."+c)] = true
		}
		rows.Close()
	}
	return out
}

func postgresColumns(t *testing.T, db *sql.DB, schema string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = $1`, schema)
	if err != nil {
		t.Fatalf("information_schema: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var tbl, col string
		if err := rows.Scan(&tbl, &col); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[strings.ToLower(tbl+"."+col)] = true
	}
	return out
}

// The schema must contain the columns the migrations named, not the types they
// were declared with.
//
// This is the assertion that would have caught the type-position bug. Before
// the fix, `timestamp TIMESTAMPTZ NOT NULL` produced a NOT NULL column named
// `TEXT` and no `timestamp` column — and the migration applied cleanly, so
// counting tables reported success.
func TestColumnsKeepTheirNamesOnSQLite(t *testing.T) {
	conn, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "lite.db")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	d := sqliteDialect{}
	if err := d.EnsureChainLock(context.Background(), conn); err != nil {
		t.Fatalf("chain lock: %v", err)
	}
	applyMigrations(t, conn, d)

	cols := sqliteColumns(t, conn)

	// The column that exposed the bug.
	if !cols["log_entries.timestamp"] {
		t.Error("log_entries has no `timestamp` column — a type name was substituted for the identifier")
	}
	// And no table anywhere may have a column named after a type, which is what
	// the substitution produces.
	for k := range cols {
		switch strings.ToLower(k[strings.Index(k, ".")+1:]) {
		case "text", "jsonb", "timestamptz", "integer":
			t.Errorf("%s: a column is named after a type, which is what the "+
				"identifier-blind rewrite produced", k)
		}
	}
}
