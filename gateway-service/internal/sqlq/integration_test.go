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
// This used to record a limitation: local execution has to fetch every row an
// aggregate covers, and Quickwit caps a search at max_hits = 10_000, so
// `SELECT count(*) FROM logs` over the 5.1M-record benchmark corpus could not
// be answered at all. Push-down fixed that by asking the store for the answer
// instead of the rows, so the assertion is now the real one: the count is
// exact, and it covers the whole corpus rather than a page of it.
func TestLiveAggregationOverTheFullCorpus(t *testing.T) {
	engine := liveEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := engine.Query(ctx, "default", "SELECT count(*) AS n FROM logs")
	if err != nil {
		t.Fatalf("count over the full corpus: %v", err)
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
		t.Fatalf("want one scalar, got %v", res.Rows)
	}
	n := toInt64(res.Rows[0][0])
	// The corpus is millions of rows; anything at or below Quickwit's 10k page
	// size would mean we counted a page and called it a total, which is the
	// failure mode that matters — a wrong count looks exactly like a right one.
	if n <= 10000 {
		t.Fatalf("count came back as %d, which is at or below the 10k page limit: "+
			"this is a truncated page reported as a total", n)
	}
	// And nothing was pulled into the engine to get it.
	if res.Scanned != 0 {
		t.Errorf("push-down moved %d rows; it should move none", res.Scanned)
	}
	t.Logf("count(*) over the full corpus = %d in %s, 0 rows moved", n, res.Elapsed.Round(time.Millisecond))
}

// The high-cardinality group-by, likewise over the whole corpus.
func TestLiveGroupByOverTheFullCorpus(t *testing.T) {
	engine := liveEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := engine.Query(ctx, "default",
		"SELECT service_name, count(*) AS n FROM logs GROUP BY service_name ORDER BY n DESC LIMIT 10")
	if err != nil {
		t.Fatalf("group-by over the full corpus: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatal("group-by returned no buckets")
	}
	if len(res.Rows) > 10 {
		t.Fatalf("LIMIT 10 was not honoured: %d buckets", len(res.Rows))
	}
	var total int64
	for _, r := range res.Rows {
		total += toInt64(r[1])
	}
	// Ten buckets of a multi-million-row corpus should still be substantial;
	// if this only adds up to a page, the aggregation was done over a page.
	if total <= 10000 {
		t.Fatalf("top-10 buckets total %d rows, which suggests a truncated scan", total)
	}
	if res.Scanned != 0 {
		t.Errorf("push-down moved %d rows; it should move none", res.Scanned)
	}
	t.Logf("top bucket %v=%v, %d buckets covering %d rows in %s",
		res.Rows[0][0], res.Rows[0][1], len(res.Rows), total, res.Elapsed.Round(time.Millisecond))
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
