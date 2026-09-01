package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// String literals must survive translation byte-for-byte.
//
// This is the failure mode with no symptom: every rule in rewrite.go is a regex
// over statement text, so applied to a whole statement they rewrite the *words*
// inside literals too. `VALUES ('the jsonb column')` became
// `VALUES ('the TEXT column')` — valid SQL, applied cleanly, green CI, wrong
// data in the row. Six of seven of these were corrupted before segments.go.
func TestRewriteLeavesStringLiteralsAlone(t *testing.T) {
	for _, stmt := range []string{
		`INSERT INTO notes (body) VALUES ('cast with a::b inside')`,
		`INSERT INTO notes (body) VALUES ('call now() when ready')`,
		`INSERT INTO notes (body) VALUES ('the jsonb column')`,
		`INSERT INTO notes (body) VALUES ('DEFAULT true means yes')`,
		`INSERT INTO notes (body) VALUES ('a timestamp value')`,
		`INSERT INTO notes (body) VALUES ('a uuid and a BIGSERIAL')`,
		`INSERT INTO abac_policies (condition) VALUES ('role == "admin" AND uuid')`,
		// An embedded quote must not end the literal early.
		`INSERT INTO notes (body) VALUES ('it''s a jsonb column')`,
		// Comments are not code either.
		"-- the jsonb column\nSELECT 1",
	} {
		if got := rewriteForSQLite(stmt); got != stmt {
			t.Errorf("literal corrupted\n  in:  %s\n  out: %s", stmt, got)
		}
	}
}

// And the code around a literal must still be translated.
func TestRewriteStillTranslatesCodeBesideLiterals(t *testing.T) {
	in := `ALTER TABLE t ADD COLUMN meta JSONB DEFAULT '{"note":"jsonb"}'`
	got := rewriteForSQLite(in)
	if !strings.Contains(got, "ADD COLUMN meta TEXT") {
		t.Errorf("the JSONB *type* was not translated: %s", got)
	}
	if !strings.Contains(got, `'{"note":"jsonb"}'`) {
		t.Errorf("the literal was altered: %s", got)
	}
}

// The same guarantee for query-time placeholder rewriting, where corruption is
// worse: it shifts every parameter after the literal, so arguments land in the
// wrong columns.
func TestPlaceholderRewriteLeavesLiteralsAlone(t *testing.T) {
	in := `SELECT * FROM t WHERE note = 'costs $5 and $10' AND id = $1 AND b = $2`
	want := `SELECT * FROM t WHERE note = 'costs $5 and $10' AND id = ?1 AND b = ?2`
	if got := RewritePlaceholders(in); got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// The scanner must classify each construct correctly, since everything above
// depends on it.
func TestScanSegmentsClassifies(t *testing.T) {
	segs := scanSegments(`SELECT 'lit', "ident" -- trailing
FROM t`)
	var literals []string
	for _, s := range segs {
		if !s.code {
			literals = append(literals, strings.TrimSpace(s.text))
		}
	}
	want := []string{`'lit'`, `"ident"`, `-- trailing`}
	if len(literals) != len(want) {
		t.Fatalf("found %v, want %v", literals, want)
	}
	for i := range want {
		if literals[i] != want[i] {
			t.Errorf("segment %d = %q, want %q", i, literals[i], want[i])
		}
	}
}

// Reassembly must be lossless regardless of content: whatever the scanner does,
// concatenating its segments has to reproduce the input exactly.
func TestScanSegmentsIsLossless(t *testing.T) {
	for _, in := range []string{
		`SELECT 1`,
		`SELECT 'a''b' FROM "t" -- note
/* block */ WHERE x = $1`,
		`DO $$ BEGIN NULL; END $$`,
		`SELECT 'unterminated`,
		``,
	} {
		var b strings.Builder
		for _, s := range scanSegments(in) {
			b.WriteString(s.text)
		}
		if b.String() != in {
			t.Errorf("lossy\n  in:  %q\n  out: %q", in, b.String())
		}
	}
}

// A type name used as a *column* name must survive.
//
// Regression, HIGH: `\btimestamp\b` → TEXT could not tell a type from an
// identifier, and log-service's schema has a column called `timestamp`:
//
//	timestamp   TIMESTAMPTZ NOT NULL,   ->  TEXT   TEXT NOT NULL,
//	ON log_entries (timestamp DESC)     ->  ON log_entries (TEXT DESC)
//
// SQLite accepts both. The table ends up with a NOT NULL column literally named
// TEXT and no `timestamp` column — migration applies, nothing errors, every
// query naming `timestamp` then fails against a schema that looks correct.
func TestTypeNamesUsedAsIdentifiersSurvive(t *testing.T) {
	cases := []struct{ in, want string }{
		// The column name is preserved; the type beside it is translated.
		{"timestamp   TIMESTAMPTZ NOT NULL,", "timestamp   TEXT NOT NULL,"},
		{"uuid UUID PRIMARY KEY", "uuid TEXT PRIMARY KEY"},
		{"jsonb JSONB", "jsonb TEXT"},
		// An identifier in an expression is not a type at all.
		{"CREATE INDEX i ON log_entries (timestamp DESC)", "CREATE INDEX i ON log_entries (timestamp DESC)"},
		{"SELECT timestamp FROM t ORDER BY timestamp", "SELECT timestamp FROM t ORDER BY timestamp"},
		// Ordinary type positions still translate.
		{"created_at TIMESTAMPTZ DEFAULT now()", "created_at TEXT DEFAULT CURRENT_TIMESTAMP"},
		{"meta JSONB", "meta TEXT"},
		{"id UUID", "id TEXT"},
		{"ts TIMESTAMP WITHOUT TIME ZONE", "ts TEXT"},
		{"ts TIMESTAMP WITH TIME ZONE", "ts TEXT"},
	}
	for _, c := range cases {
		if got := rewriteForSQLite(c.in); got != c.want {
			t.Errorf("\n  in:   %s\n  got:  %s\n  want: %s", c.in, got, c.want)
		}
	}
}

// CONCURRENTLY must be stripped from the UNIQUE form too, not just the plain
// one — the rule read as though it covered the construct and covered half.
func TestConcurrentlyStrippedFromUniqueIndexes(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"CREATE INDEX CONCURRENTLY IF NOT EXISTS i ON t (a)", "CREATE INDEX IF NOT EXISTS i ON t (a)"},
		{"CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS i ON t (a)", "CREATE UNIQUE INDEX IF NOT EXISTS i ON t (a)"},
	} {
		if got := rewriteForSQLite(c.in); got != c.want {
			t.Errorf("\n  got:  %s\n  want: %s", got, c.want)
		}
	}
}

// A statement-spanning rule must not be defeated by a literal in the span.
//
// Regression: `fixInsertSelectUpsert` ran per code segment, so any literal
// between INSERT and ON CONFLICT split the run and the fix silently did not
// happen — leaving a statement SQLite rejects. The existing migration has no
// literal there, which is the only reason CI was green.
func TestUpsertFixSurvivesLiteralsInTheSpan(t *testing.T) {
	for _, in := range []string{
		`INSERT INTO tenants (id,name) SELECT DISTINCT tenant_id, tenant_id FROM users ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO roles (name, permissions) SELECT 'viewer', perms FROM seed ON CONFLICT (name) DO NOTHING`,
		"INSERT INTO roles (name, p) SELECT n, p FROM seed -- a comment\nON CONFLICT (name) DO NOTHING",
	} {
		got := rewriteForSQLite(in)
		if !strings.Contains(got, "WHERE true") {
			t.Errorf("upsert fix skipped:\n  in:  %s\n  out: %s", in, got)
		}
	}

	// A SELECT that already filters must not gain a second WHERE.
	filtered := `INSERT INTO t (a) SELECT a FROM s WHERE a > 0 ON CONFLICT (a) DO NOTHING`
	if got := rewriteForSQLite(filtered); strings.Count(got, "WHERE") != 1 {
		t.Errorf("a filtered SELECT gained an extra WHERE: %s", got)
	}
	// And a literal containing the word must not count as one.
	lit := `INSERT INTO t (a) SELECT 'WHERE' FROM s ON CONFLICT (a) DO NOTHING`
	if got := rewriteForSQLite(lit); !strings.Contains(got, "WHERE true") {
		t.Errorf("a literal containing WHERE suppressed the fix: %s", got)
	}
}

// A CREATE EXTENSION statement must be replaced entirely, not partially.
//
// Regression: extension names are quoted, and a quoted identifier is a literal
// to the segment scanner — so a per-segment rule rewrote only
// `CREATE EXTENSION IF NOT EXISTS ` and left `"pgcrypto"` behind, producing
// `SELECT 1"pgcrypto"`. SQLite reads that as `SELECT 1 AS "pgcrypto"` and
// accepts it, so every test passed. Working by coincidence is not working.
func TestCreateExtensionIsReplacedWholly(t *testing.T) {
	for _, in := range []string{
		`CREATE EXTENSION IF NOT EXISTS "pgcrypto"`,
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE EXTENSION pgcrypto`,
		`CREATE EXTENSION IF NOT EXISTS "pgcrypto" WITH SCHEMA public`,
	} {
		got := rewriteForSQLite(in)
		if got != "SELECT 1" {
			t.Errorf("\n  in:  %s\n  got: %q, want \"SELECT 1\"", in, got)
		}
	}
	// A leading comment is kept; only the statement is replaced.
	withComment := "-- for gen_random_uuid() fallback\nCREATE EXTENSION IF NOT EXISTS \"pgcrypto\""
	if got := rewriteForSQLite(withComment); !strings.HasSuffix(got, "SELECT 1") ||
		!strings.Contains(got, "-- for gen_random_uuid") {
		t.Errorf("comment or replacement lost: %q", got)
	}
}

// A reused parameter must stay one parameter.
//
// Postgres allows `WHERE a = $1 OR b = $1` with a single argument. Rewriting
// both to a bare `?` makes them two positional placeholders, and the driver
// reports `missing argument with index 2`. Six statements in this repository
// already reuse a placeholder — `$3 = ” OR kind = $3` among them — so this
// would have broken the first time P1.6 sent real queries through Rebind.
func TestRewrittenPlaceholdersPreserveReuse(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`SELECT * FROM t WHERE a = $1 OR b = $1`, `SELECT * FROM t WHERE a = ?1 OR b = ?1`},
		{`SELECT $2, $1`, `SELECT ?2, ?1`},
		{`UPDATE t SET a = $1 WHERE id = $2 AND owner = $1`, `UPDATE t SET a = ?1 WHERE id = ?2 AND owner = ?1`},
	} {
		if got := RewritePlaceholders(c.in); got != c.want {
			t.Errorf("\n  in:   %s\n  got:  %s\n  want: %s", c.in, got, c.want)
		}
	}
}

// And the rewritten form must actually execute with the Postgres argument count.
func TestReusedPlaceholderExecutesOnSQLite(t *testing.T) {
	conn, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "p.db")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE t (a TEXT, b TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO t VALUES ('x','y')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	q := RewritePlaceholders(`SELECT a FROM t WHERE a = $1 OR b = $1`)
	var got string
	// One argument, as Postgres would take.
	if err := conn.QueryRowContext(ctx, q, "x").Scan(&got); err != nil {
		t.Fatalf("reused placeholder failed with a single argument: %v (query: %s)", err, q)
	}
	if got != "x" {
		t.Errorf("got %q, want x", got)
	}
}
