package sqlq

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// Integration tests against real Quickwit, ClickHouse and Postgres.
//
// These exist because the unit tests could not have caught the bug that
// motivated them. ClickHouseScanner sent no credentials, so every
// ClickHouse-backed relation failed against a real server with
//
//	Code: 516. DB::Exception: default: Authentication failed
//
// while the unit test passed, because httptest does not check credentials.
// Fakes verify the shape of a request; only a real server verifies that the
// request is one it will actually answer.
//
// Skipped unless PULSETRACE_STORES_IT is set:
//
//	PULSETRACE_STORES_IT=1 go test ./internal/sqlq/ -run Live -v
func storeEnv(t *testing.T) (chURL, chUser, chPass, qwURL, pgDSN string) {
	t.Helper()
	if os.Getenv("PULSETRACE_STORES_IT") == "" {
		t.Skip("set PULSETRACE_STORES_IT=1 with a running stack to exercise the real stores")
	}
	get := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}
	return get("CLICKHOUSE_URL", "http://127.0.0.1:8123"),
		get("CLICKHOUSE_USER", "pulsetrace"),
		get("CLICKHOUSE_PASSWORD", "pulsetrace_secret"),
		get("QUICKWIT_URL", "http://127.0.0.1:7280"),
		get("DATABASE_URL", "postgres://pulsetrace:pulsetrace_secret@127.0.0.1:5434/pulsetrace?sslmode=disable")
}

// Every scanner must be able to talk to its store and come back without an
// error. Row counts are not asserted — a store may legitimately be empty — but
// an error never is, because that is what a misconfigured client produces.
func TestLiveScannersReachTheirStores(t *testing.T) {
	chURL, chUser, chPass, qwURL, pgDSN := storeEnv(t)
	cat := DefaultCatalog()

	db, err := sql.Open("postgres", pgDSN)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, name := range cat.Names() {
		rel, _ := cat.Lookup("", name)
		var s Scanner
		switch rel.Store {
		case StoreLogs:
			s = &QuickwitScanner{Rel: rel, URL: qwURL, Index: "pulsetrace-logs"}
		case StoreAnalytics:
			s = &ClickHouseScanner{Rel: rel, URL: chURL, User: chUser, Pass: chPass}
		case StoreMeta:
			s = &PostgresScanner{Rel: rel, DB: db}
		}
		t.Run(name, func(t *testing.T) {
			rows, err := s.Scan(ctx, "default", 5)
			if err != nil {
				t.Fatalf("%s (%s): %v", name, rel.Store, err)
			}
			// The projection must match the catalog, or user SQL that validated
			// against the catalog will not find its columns at execution.
			if len(rows.Columns) != len(rel.Columns) {
				t.Errorf("%s: got %d columns %v, catalog declares %d %v",
					name, len(rows.Columns), rows.Columns, len(rel.Columns), rel.Columns)
			}
			t.Logf("%-18s %-9s %d rows, columns %v", name, rel.Store, len(rows.Values), rows.Columns)
		})
	}
}

// The end-to-end path, against the real corpus.
//
// This test records a limitation rather than a success, because that is what
// the stack currently does. Local execution means every row an aggregate covers
// must be fetched first, and Quickwit caps a search at 10,000 hits:
//
//	Invalid argument: max value for max_hits is 10_000, but got 500001
//
// So `SELECT count(*) FROM logs` over the 5.1M-record benchmark corpus cannot
// be answered by this path at all. D3 — the dimension this whole phase exists
// to close — is therefore *not* closed by making the query expressible; it
// needs the aggregation pushed into the store, which is P3.5.
//
// The property that matters in the meantime is the one asserted here: at a
// scale it cannot serve, the engine fails **loudly**. An aggregate computed
// over a silently truncated sample would be a correctness bug and strictly
// worse than an error, because a wrong count looks exactly like a right one.
func TestLiveAggregationOverTheFullCorpusFailsLoudlyRatherThanTruncating(t *testing.T) {
	engine := liveEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := engine.Query(ctx, "default", "SELECT count(*) AS n FROM logs")
	if err == nil {
		t.Fatalf("returned an answer (%v) for a corpus larger than it can fetch; "+
			"a truncated aggregate is worse than an error", res.Rows)
	}
	t.Logf("refused as expected: %v", err)
}

// The same shapes over relations small enough to fetch must genuinely work —
// otherwise the limitation above would be indistinguishable from the feature
// simply being broken.
func TestLiveEngineRunsRealQueriesWithinBudget(t *testing.T) {
	engine := liveEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	for _, tc := range []struct{ name, sql string }{
		{"aggregation", "SELECT count(*) AS n FROM incidents"},
		{"group-by", "SELECT status, count(*) AS n FROM incidents GROUP BY status ORDER BY n DESC"},
		{"cross-store join", "SELECT i.severity, count(*) AS n FROM incidents i " +
			"JOIN synthetic_results s ON i.status = s.check_name GROUP BY i.severity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := engine.Query(ctx, "default", tc.sql)
			if err != nil {
				t.Fatalf("%s: %v", tc.sql, err)
			}
			t.Logf("%-18s %d rows returned, %d scanned, %s",
				tc.name, len(res.Rows), res.Scanned, res.Elapsed.Round(time.Millisecond))
		})
	}
}

// liveEngine builds an engine over the real stores with the shipped defaults.
func liveEngine(t *testing.T) *Engine {
	t.Helper()
	chURL, chUser, chPass, qwURL, pgDSN := storeEnv(t)
	cat := DefaultCatalog()

	db, err := sql.Open("postgres", pgDSN)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	var scanners []Scanner
	for _, name := range cat.Names() {
		rel, _ := cat.Lookup("", name)
		switch rel.Store {
		case StoreLogs:
			scanners = append(scanners, &QuickwitScanner{Rel: rel, URL: qwURL, Index: "pulsetrace-logs"})
		case StoreAnalytics:
			scanners = append(scanners, &ClickHouseScanner{Rel: rel, URL: chURL, User: chUser, Pass: chPass})
		case StoreMeta:
			scanners = append(scanners, &PostgresScanner{Rel: rel, DB: db})
		}
	}
	// The shipped defaults, deliberately — a budget invented for the test would
	// answer a different question than "does a user's query work".
	engine, err := NewEngine(cat, DefaultPolicy(), DefaultBudget(), scanners...)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return engine
}
