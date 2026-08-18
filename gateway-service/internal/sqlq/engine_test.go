package sqlq

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// recordingScanner wraps a scanner and records every tenant it was asked for.
//
// The isolation claim is not "no other tenant's rows came back" — that is an
// assertion about output, and output can be filtered by accident. It is "no
// other tenant's rows were ever fetched". Recording the requests is the only
// way to assert the stronger thing.
type recordingScanner struct {
	inner Scanner
	mu    sync.Mutex
	asked []string
}

func (r *recordingScanner) Relation() Relation { return r.inner.Relation() }

func (r *recordingScanner) Scan(ctx context.Context, tenantID string, limit int) (*Rows, error) {
	r.mu.Lock()
	r.asked = append(r.asked, tenantID)
	r.mu.Unlock()
	return r.inner.Scan(ctx, tenantID, limit)
}

func (r *recordingScanner) tenants() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

// testEngine builds an engine over every catalog relation, with acme and
// initech data in each. Every relation needs a scanner or NewEngine refuses.
func testEngine(t *testing.T) (*Engine, map[string]*recordingScanner) {
	t.Helper()
	cat := DefaultCatalog()
	recs := map[string]*recordingScanner{}
	var scanners []Scanner
	for _, name := range cat.Names() {
		rel, _ := cat.Lookup("", name)
		static := &StaticScanner{Rel: rel, ByTenant: map[string]*Rows{
			"acme":    rowsFor(rel, "acme"),
			"initech": rowsFor(rel, "initech"),
		}}
		rec := &recordingScanner{inner: static}
		recs[name] = rec
		scanners = append(scanners, rec)
	}
	e, err := NewEngine(cat, DefaultPolicy(), DefaultBudget(), scanners...)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e, recs
}

// rowsFor makes rows whose every cell is stamped with the owning tenant, so a
// leak is visible in any column rather than only in one designated field.
func rowsFor(rel Relation, tenant string) *Rows {
	r := &Rows{Columns: rel.Columns}
	for i := 0; i < 3; i++ {
		var row []any
		for _, c := range rel.Columns {
			row = append(row, fmt.Sprintf("%s-%s-%d", tenant, c, i))
		}
		r.Values = append(r.Values, row)
	}
	return r
}

// The central test. Whatever the user writes, only their own tenant is ever
// fetched — because the statement is not an input to fetching.
func TestNoStatementCanCauseAnotherTenantToBeScanned(t *testing.T) {
	attacks := []string{
		"SELECT * FROM logs",
		"SELECT * FROM logs WHERE service_name = 'initech'",
		"SELECT * FROM logs UNION ALL SELECT timestamp, service_name, level, message, trace_id FROM logs",
		"WITH x AS (SELECT * FROM logs) SELECT * FROM x",
		"SELECT l.message FROM logs l JOIN traces t ON l.trace_id = t.trace_id",
		"SELECT count(*) FROM logs WHERE service_name IN (SELECT service_name FROM traces)",
		"SELECT * FROM logs -- initech",
		"SELECT * FROM logs /* tenant_id = 'initech' */",
		"SELECT 'initech' AS tenant_id, message FROM logs",
	}

	for _, sql := range attacks {
		t.Run(sql, func(t *testing.T) {
			e, recs := testEngine(t)
			res, err := e.Query(context.Background(), "acme", sql)
			if err != nil {
				t.Fatalf("query failed: %v", err)
			}
			// Nothing was ever requested for another tenant.
			for name, rec := range recs {
				for _, got := range rec.tenants() {
					if got != "acme" {
						t.Fatalf("scanner %q was asked for tenant %q", name, got)
					}
				}
			}
			// And nothing belonging to another tenant came back. A literal the
			// user typed is theirs to type; a *value* stamped initech is not.
			for _, row := range res.Rows {
				for _, v := range row {
					s, ok := v.(string)
					if ok && strings.HasPrefix(s, "initech-") {
						t.Fatalf("result contains another tenant's data: %q", s)
					}
				}
			}
		})
	}
}

func TestEmptyTenantIsRefused(t *testing.T) {
	e, recs := testEngine(t)
	for _, tenant := range []string{"", "   "} {
		if _, err := e.Query(context.Background(), tenant, "SELECT * FROM logs"); err == nil {
			t.Fatalf("empty tenant %q was accepted", tenant)
		}
	}
	// And it failed closed — before any scanner ran.
	for name, rec := range recs {
		if got := rec.tenants(); len(got) != 0 {
			t.Fatalf("scanner %q ran for an empty tenant: %v", name, got)
		}
	}
}

func TestEachTenantSeesOnlyItsOwnRows(t *testing.T) {
	for _, tenant := range []string{"acme", "initech"} {
		e, _ := testEngine(t)
		res, err := e.Query(context.Background(), tenant, "SELECT service_name FROM logs")
		if err != nil {
			t.Fatalf("%s: %v", tenant, err)
		}
		if len(res.Rows) != 3 {
			t.Fatalf("%s: got %d rows, want 3", tenant, len(res.Rows))
		}
		for _, row := range res.Rows {
			if s := row[0].(string); !strings.HasPrefix(s, tenant+"-") {
				t.Fatalf("%s: saw %q", tenant, s)
			}
		}
	}
}

// A tenant with no data gets an empty result, not an error and not someone
// else's rows.
func TestUnknownTenantGetsNothing(t *testing.T) {
	e, _ := testEngine(t)
	res, err := e.Query(context.Background(), "no-such-tenant", "SELECT service_name FROM logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("got %d rows for a tenant with no data", len(res.Rows))
	}
}

// ── The capability this slice exists to deliver ──────────────────────────────

// These are the two benchmark classes that were "not expressible" against
// PulseTrace at any latency — dimension D3. They are the reason for the whole
// phase, so they get a test that runs them rather than a claim that they work.
func TestPreviouslyInexpressibleQueryClassesNowRun(t *testing.T) {
	e, _ := testEngine(t)

	t.Run("full-scan aggregation", func(t *testing.T) {
		res, err := e.Query(context.Background(), "acme",
			"SELECT count(*) AS n FROM logs")
		if err != nil {
			t.Fatalf("aggregation failed: %v", err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(res.Rows))
		}
	})

	t.Run("high-cardinality group-by", func(t *testing.T) {
		res, err := e.Query(context.Background(), "acme",
			"SELECT service_name, count(*) AS n FROM logs GROUP BY service_name ORDER BY n DESC LIMIT 10")
		if err != nil {
			t.Fatalf("group-by failed: %v", err)
		}
		if len(res.Rows) == 0 {
			t.Fatal("group-by returned nothing")
		}
	})

	t.Run("cross-store join", func(t *testing.T) {
		// logs is served by Quickwit and traces by ClickHouse; a join across them
		// is something neither store can do alone and OpenObserve cannot do at
		// all. Here it is just a join, because both sides are already local.
		if _, err := e.Query(context.Background(), "acme",
			"SELECT l.message, t.span_name FROM logs l JOIN traces t ON l.trace_id = t.trace_id"); err != nil {
			t.Fatalf("cross-store join failed: %v", err)
		}
	})
}

// ── Engine construction ──────────────────────────────────────────────────────

// A relation that resolves but cannot be read would fail at runtime on a query
// that passed validation. Refuse to start instead.
func TestEngineRefusesToStartWithAnUnservedRelation(t *testing.T) {
	cat := DefaultCatalog()
	rel, _ := cat.Lookup("", "logs")
	_, err := NewEngine(cat, DefaultPolicy(), DefaultBudget(),
		&StaticScanner{Rel: rel, ByTenant: map[string]*Rows{}})
	if err == nil {
		t.Fatal("engine started with relations that have no scanner")
	}
	if !strings.Contains(err.Error(), "traces") {
		t.Fatalf("error should name the unserved relations, got: %v", err)
	}
}

func TestBudgetCapsTotalRowsAcrossRelations(t *testing.T) {
	cat := DefaultCatalog()
	var scanners []Scanner
	for _, name := range cat.Names() {
		rel, _ := cat.Lookup("", name)
		scanners = append(scanners, &StaticScanner{Rel: rel, ByTenant: map[string]*Rows{
			"acme": rowsFor(rel, "acme"),
		}})
	}
	// Two relations at 3 rows each exceeds a total cap of 5.
	b := DefaultBudget()
	b.MaxTotalRows = 5
	e, err := NewEngine(cat, DefaultPolicy(), b, scanners...)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	_, err = e.Query(context.Background(), "acme",
		"SELECT l.message FROM logs l JOIN traces t ON l.trace_id = t.trace_id")
	if err == nil {
		t.Fatal("query exceeded the total-row budget but was allowed")
	}
	var be *BudgetError
	if !asBudget(err, &be) {
		t.Fatalf("want a BudgetError, got %T: %v", err, err)
	}
	if be.Limit != "total_rows" {
		t.Fatalf("want total_rows, got %s", be.Limit)
	}
}

func asBudget(err error, target **BudgetError) bool {
	for err != nil {
		if be, ok := err.(*BudgetError); ok {
			*target = be
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
