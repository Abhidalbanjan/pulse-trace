package sqlq

import (
	"strings"
	"testing"
)

// The escape suite.
//
// This is the deliverable of the query-core slice, not an accompaniment to it.
// Opening SQL to users is the single largest expansion of attack surface in the
// product, and the argument that it is safe has to be executable.
//
// Each case below is a way someone would actually try to reach data they are
// not entitled to, or to make the server do work it should not. A case asserts
// one of two things: the statement is refused with a specific reason, or it is
// accepted and provably reads nothing outside the catalog. "It errored somehow"
// is not an assertion — a query refused for the wrong reason is a query that
// will be admitted the moment that incidental reason stops applying.

func mustReject(t *testing.T, sql string, want Reason) {
	t.Helper()
	_, err := Validate(sql, DefaultCatalog(), DefaultPolicy())
	if err == nil {
		t.Fatalf("ACCEPTED but must be refused:\n  %s", sql)
	}
	got, ok := ReasonOf(err)
	if !ok {
		t.Fatalf("refused with a non-rejection error: %v", err)
	}
	if got != want {
		t.Fatalf("refused for the wrong reason:\n  sql:  %s\n  want: %s\n  got:  %s (%v)", sql, want, got, err)
	}
}

// mustRejectAnyOf is for statements the current grammar refuses before policy
// runs. Listing the acceptable reasons keeps "our rule caught it" distinct from
// "the parser happened not to understand it" — the second is a weaker property
// and should never be mistaken for the first.
func mustRejectAnyOf(t *testing.T, sql string, want ...Reason) {
	t.Helper()
	_, err := Validate(sql, DefaultCatalog(), DefaultPolicy())
	if err == nil {
		t.Fatalf("ACCEPTED but must be refused:\n  %s", sql)
	}
	got, ok := ReasonOf(err)
	if !ok {
		t.Fatalf("refused with a non-rejection error: %v", err)
	}
	for _, w := range want {
		if got == w {
			return
		}
	}
	t.Fatalf("refused for an unexpected reason:\n  sql:  %s\n  want one of: %v\n  got:  %s", sql, want, got)
}

func mustAccept(t *testing.T, sql string, wantRelations ...string) *Analysis {
	t.Helper()
	a, err := Validate(sql, DefaultCatalog(), DefaultPolicy())
	if err != nil {
		t.Fatalf("REFUSED but must be accepted:\n  %s\n  %v", sql, err)
	}
	var got []string
	for _, r := range a.Relations {
		got = append(got, r.Name)
	}
	if strings.Join(got, ",") != strings.Join(wantRelations, ",") {
		t.Fatalf("relations mismatch for %s:\n  want %v\n  got  %v", sql, wantRelations, got)
	}
	return a
}

// ── Reaching physical storage ────────────────────────────────────────────────
//
// The catalog exposes logical names only. Knowing the physical schema must buy
// an attacker nothing, so these use the real table names from the running
// system.

func TestPhysicalTablesAreUnreachable(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM pulsetrace.otel_logs",
		"SELECT * FROM pulsetrace.rum_events",
		"SELECT * FROM pulsetrace.synthetic_results",
	} {
		mustReject(t, sql, ReasonQualifiedName)
	}
	// Unqualified physical names do not resolve either — the catalog is an
	// allowlist, so an unknown name is refused rather than passed through.
	for _, sql := range []string{
		"SELECT * FROM otel_logs",
		"SELECT * FROM otel_traces",
		"SELECT * FROM synthetic_targets",
		"SELECT * FROM users",
	} {
		mustReject(t, sql, ReasonUnknownRelation)
	}
}

func TestSystemSchemasAreUnreachable(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM information_schema.tables",
		"SELECT * FROM mysql.user",
		"SELECT * FROM system.tables",
		"SELECT * FROM system.parts",
		"SELECT * FROM pg_catalog.pg_tables",
	} {
		mustReject(t, sql, ReasonQualifiedName)
	}
}

// A JOIN is the classic way to smuggle a second, unrelated relation into an
// otherwise innocuous query.
func TestJoinToSystemTableIsRefused(t *testing.T) {
	mustReject(t,
		"SELECT l.message FROM logs l JOIN information_schema.tables t ON 1=1",
		ReasonQualifiedName)
	mustReject(t,
		"SELECT l.message FROM logs l JOIN otel_logs o ON l.trace_id = o.TraceId",
		ReasonUnknownRelation)
}

// ── Statement-shape attacks ──────────────────────────────────────────────────

func TestStackedStatementsAreRefused(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1 FROM logs; SELECT * FROM information_schema.tables",
		"SELECT 1 FROM logs; DROP TABLE logs",
		// The second statement being harmless does not make stacking acceptable.
		"SELECT 1 FROM logs; SELECT 2 FROM logs",
	} {
		mustReject(t, sql, ReasonMultipleStmts)
	}
}

func TestNonSelectStatementsAreRefused(t *testing.T) {
	for _, sql := range []string{
		"INSERT INTO logs (message) VALUES ('x')",
		"UPDATE logs SET message = 'x'",
		"DELETE FROM logs",
		"DROP TABLE logs",
		"CREATE TABLE t (a INT)",
		"ALTER TABLE logs ADD COLUMN a INT",
		"TRUNCATE TABLE logs",
		"GRANT ALL ON *.* TO 'x'",
		"SET GLOBAL max_connections = 1",
		"SHOW TABLES",
		"USE mysql",
	} {
		mustReject(t, sql, ReasonNotSelect)
	}
}

// Comments are the oldest trick in the list, and the reason the previous
// textual guard fails: it accepts any query whose *text* contains "TenantID",
// which a comment supplies for free. Under an AST the comment is not a
// reference to anything.
func TestCommentInjectionCannotSmuggleAReference(t *testing.T) {
	// Exactly the strings that defeat clickhouse.go's assertTenantScoped.
	mustReject(t, "SELECT * FROM otel_logs -- TenantID", ReasonUnknownRelation)
	mustReject(t, "SELECT * FROM pulsetrace.rum_events /* tenant.id */", ReasonQualifiedName)
	mustReject(t, "SELECT * FROM otel_traces WHERE x = 'TenantID'", ReasonUnknownRelation)

	// A comment in an otherwise valid query is simply ignored, and cannot add a
	// relation to the analysis.
	a := mustAccept(t, "SELECT message /* TenantID */ FROM logs -- system.tables", "logs")
	if len(a.Relations) != 1 {
		t.Fatalf("comment contributed a relation: %v", a.Relations)
	}
}

// ── Set operations and subqueries ────────────────────────────────────────────

func TestUnionCannotReachOutsideTheCatalog(t *testing.T) {
	mustReject(t,
		"SELECT message FROM logs UNION ALL SELECT name FROM information_schema.tables",
		ReasonQualifiedName)
	mustReject(t,
		"SELECT message FROM logs UNION ALL SELECT Body FROM otel_logs",
		ReasonUnknownRelation)

	// A union across catalog relations is legitimate and must be accepted, with
	// both relations reported so the planner binds a tenant for each.
	mustAccept(t,
		"SELECT service_name FROM logs UNION ALL SELECT service_name FROM traces",
		"logs", "traces")
}

func TestCorrelatedSubqueryIsBoundedAndScoped(t *testing.T) {
	mustReject(t,
		"SELECT service_name FROM logs l WHERE EXISTS (SELECT 1 FROM system.parts p WHERE p.table = l.service_name)",
		ReasonQualifiedName)

	mustAccept(t,
		"SELECT service_name FROM logs l WHERE EXISTS (SELECT 1 FROM traces t WHERE t.service_name = l.service_name)",
		"logs", "traces")
}

func TestSubqueryDepthIsBounded(t *testing.T) {
	// Depth 4 with a limit of 3.
	sql := "SELECT service_name FROM logs WHERE service_name IN (" +
		"SELECT service_name FROM traces WHERE service_name IN (" +
		"SELECT service_name FROM metrics WHERE service_name IN (" +
		"SELECT service_name FROM logs)))"
	mustReject(t, sql, ReasonTooDeep)
}

func TestJoinCountIsBounded(t *testing.T) {
	sql := "SELECT 1 FROM logs a JOIN traces b ON 1=1 JOIN metrics c ON 1=1 " +
		"JOIN rum_events d ON 1=1 JOIN synthetic_results e ON 1=1 JOIN deployments f ON 1=1"
	mustReject(t, sql, ReasonTooManyJoins)
}

// ── CTEs ─────────────────────────────────────────────────────────────────────

// A CTE that takes a catalog relation's name makes that name mean two different
// things in one statement. Tenant binding is per relation, so that ambiguity is
// where an isolation bug would live.
func TestCTECannotShadowACatalogRelation(t *testing.T) {
	mustReject(t,
		"WITH logs AS (SELECT 1 AS x) SELECT * FROM logs",
		ReasonShadowedRelation)
	mustReject(t,
		"WITH traces AS (SELECT service_name FROM logs) SELECT * FROM traces",
		ReasonShadowedRelation)
}

func TestCTEBodyIsValidatedLikeAnyOtherQuery(t *testing.T) {
	mustReject(t,
		"WITH bad AS (SELECT * FROM system.tables) SELECT * FROM bad",
		ReasonQualifiedName)
	mustReject(t,
		"WITH bad AS (SELECT * FROM otel_logs) SELECT * FROM bad",
		ReasonUnknownRelation)

	mustAccept(t,
		"WITH recent AS (SELECT service_name FROM logs) SELECT service_name FROM recent",
		"logs")
}

// ── Exfiltration and side effects ────────────────────────────────────────────

func TestIntoOutfileIsRefused(t *testing.T) {
	// OUTFILE parses and is refused by policy — the guarantee we control.
	mustReject(t, "SELECT * FROM logs INTO OUTFILE '/tmp/x'", ReasonIntoOutfile)

	// DUMPFILE does not parse under this grammar, so policy never sees it. That
	// is still a refusal, but a weaker one: it holds because of the parser we
	// happen to use, not because of a rule we wrote, and it would evaporate
	// silently if the grammar were swapped for one that accepts it. Asserted as
	// "refused, by either layer" so the distinction stays visible instead of
	// being recorded as a policy win it is not.
	mustRejectAnyOf(t, "SELECT * FROM logs INTO DUMPFILE '/tmp/x'", ReasonIntoOutfile, ReasonSyntax)
}

func TestDeniedFunctionsAreRefused(t *testing.T) {
	for _, sql := range []string{
		"SELECT load_file('/etc/passwd') FROM logs",
		"SELECT sleep(10) FROM logs",
		"SELECT benchmark(100000000, md5('x')) FROM logs",
		"SELECT get_lock('x', 10) FROM logs",
	} {
		mustReject(t, sql, ReasonDeniedFunction)
	}
}

func TestLockingReadsAreRefused(t *testing.T) {
	mustReject(t, "SELECT * FROM logs FOR UPDATE", ReasonLocking)
	mustReject(t, "SELECT * FROM logs LOCK IN SHARE MODE", ReasonLocking)
}

// ── Lexical trickery ─────────────────────────────────────────────────────────

// Quoting must not create a second spelling that resolves differently from the
// one the policy checked.
func TestQuotedAndUnicodeIdentifiersDoNotCreateNewNames(t *testing.T) {
	// Backquoted physical name is still not in the catalog.
	mustReject(t, "SELECT * FROM `otel_logs`", ReasonUnknownRelation)
	mustReject(t, "SELECT * FROM `pulsetrace`.`otel_logs`", ReasonQualifiedName)

	// Fullwidth homoglyphs are a different identifier, not an alias for `logs`.
	mustReject(t, "SELECT * FROM \uFF4Coctets", ReasonUnknownRelation)

	// Case folding resolves to the same relation and must not produce duplicates.
	mustAccept(t, "SELECT service_name FROM LOGS", "logs")
	mustAccept(t, "SELECT service_name FROM logs UNION ALL SELECT service_name FROM LoGs", "logs")
}

func TestOversizedStatementIsRefusedBeforeParsing(t *testing.T) {
	sql := "SELECT " + strings.Repeat("1,", 20000) + "1 FROM logs"
	mustReject(t, sql, ReasonTooLarge)
}

// ── The property that matters ────────────────────────────────────────────────

// Whatever else it does, an accepted statement must never name anything the
// catalog does not define. This is asserted over the whole corpus rather than
// case by case, so a future relaxation that admits a new shape cannot quietly
// admit a new *reference*.
func TestAcceptedStatementsOnlyEverReferenceCatalogRelations(t *testing.T) {
	cat := DefaultCatalog()
	corpus := []string{
		"SELECT service_name, count(*) FROM logs GROUP BY service_name",
		"SELECT service_name FROM logs UNION ALL SELECT service_name FROM traces",
		"WITH r AS (SELECT service_name FROM logs) SELECT * FROM r",
		"SELECT l.message FROM logs l JOIN traces t ON l.trace_id = t.trace_id",
		"SELECT service_name FROM logs WHERE level = 'error' ORDER BY timestamp DESC LIMIT 100",
		"SELECT count(*) FROM logs WHERE service_name IN (SELECT service_name FROM traces)",
		"SELECT service_name FROM LOGS /* comment */ -- trailing",
	}
	known := map[string]bool{}
	for _, n := range cat.Names() {
		known[n] = true
	}
	for _, sql := range corpus {
		a, err := Validate(sql, cat, DefaultPolicy())
		if err != nil {
			t.Fatalf("corpus statement refused: %s\n  %v", sql, err)
		}
		if len(a.Relations) == 0 {
			t.Fatalf("accepted with no relations, so nothing would be tenant-bound: %s", sql)
		}
		for _, r := range a.Relations {
			if !known[r.Name] {
				t.Fatalf("accepted a non-catalog relation %q from: %s", r.Name, sql)
			}
			if !r.TenantBound {
				t.Fatalf("accepted relation %q that is not tenant-bound: %s", r.Name, sql)
			}
		}
	}
}
