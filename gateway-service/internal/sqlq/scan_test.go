package sqlq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Scanners are where this design deliberately concentrates its risk: the engine
// makes cross-tenant access unrepresentable *given* that every scanner isolates
// correctly. There is one such function per store, so they get checked directly
// rather than only through the engine.

// Every ClickHouse relation must filter by tenant, and must do it with a bind
// parameter rather than by interpolating the value.
func TestClickHouseStatementsAreTenantBound(t *testing.T) {
	cat := DefaultCatalog()
	for name, tbl := range chTables {
		rel, ok := cat.Lookup("", name)
		if !ok {
			t.Fatalf("chTables has %q, which is not a catalog relation", name)
		}
		s := &ClickHouseScanner{Rel: rel, URL: "http://ch"}
		stmt, err := s.statement(100)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(stmt, tbl.tenantPred) {
			t.Errorf("%s: statement lacks its tenant predicate\n  %s", name, stmt)
		}
		if !strings.Contains(stmt, "{tenant:String}") {
			t.Errorf("%s: tenant is not a bind parameter\n  %s", name, stmt)
		}
		if !strings.Contains(stmt, " WHERE ") {
			t.Errorf("%s: no WHERE clause at all\n  %s", name, stmt)
		}
	}
}

func TestPostgresStatementsAreTenantBound(t *testing.T) {
	cat := DefaultCatalog()
	for name := range pgTables {
		rel, ok := cat.Lookup("", name)
		if !ok {
			t.Fatalf("pgTables has %q, which is not a catalog relation", name)
		}
		s := &PostgresScanner{Rel: rel}
		stmt, err := s.statement()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(stmt, "WHERE tenant_id = $1") {
			t.Errorf("%s: statement is not tenant-bound\n  %s", name, stmt)
		}
	}
}

// Quickwit has no bind parameters, so the tenant becomes syntax. That is the
// one place in this package where a value can change a query's meaning, and it
// is guarded by refusal rather than escaping.
func TestQuickwitRefusesTenantIDsThatCouldBecomeSyntax(t *testing.T) {
	s := &QuickwitScanner{Rel: Relation{Name: "logs"}, URL: "http://qw"}

	for _, bad := range []string{
		"acme OR tenant_id:initech",
		"acme OR *",
		"*",
		"acme AND NOT tenant_id:acme",
		"acme)",
		"acme\"",
		"acme initech",
		"-acme",
		"",
		strings.Repeat("a", 51), // longer than the column permits
	} {
		if _, err := s.searchQuery(bad); err == nil {
			t.Errorf("accepted a tenant id that can alter the query: %q", bad)
		}
	}

	for _, good := range []string{"acme", "default", "tenant-1", "tenant_1", "a.b", "A1"} {
		q, err := s.searchQuery(good)
		if err != nil {
			t.Errorf("rejected a legitimate tenant id %q: %v", good, err)
			continue
		}
		if q != "tenant_id:"+good {
			t.Errorf("unexpected query for %q: %s", good, q)
		}
	}
}

// The catalog and the physical mappings must describe the same columns.
//
// A catalog column with no mapping resolves at validation and then fails or
// returns nulls at scan time. A mapped column absent from the catalog is worse:
// it is fetched and loaded into the query engine while being invisible in the
// contract, which is how a column nobody meant to expose becomes selectable.
func TestCatalogAndPhysicalMappingsAgree(t *testing.T) {
	cat := DefaultCatalog()

	for name, tbl := range chTables {
		rel, _ := cat.Lookup("", name)
		assertSameColumns(t, "clickhouse:"+name, rel.Columns, logicalNames(tbl.columns))
	}
	for name, tbl := range pgTables {
		rel, _ := cat.Lookup("", name)
		assertSameColumns(t, "postgres:"+name, rel.Columns, logicalNames(tbl.columns))
	}
}

// Every catalog relation must have a physical mapping somewhere, or it resolves
// for users and cannot be read. `metrics` was removed from the catalog for
// exactly this reason and this test is what keeps it out until it has a home.
func TestEveryCatalogRelationHasAPhysicalMapping(t *testing.T) {
	cat := DefaultCatalog()
	for _, name := range cat.Names() {
		rel, _ := cat.Lookup("", name)
		_, ch := chTables[name]
		_, pg := pgTables[name]
		qw := rel.Store == StoreLogs // the Quickwit scanner serves the log relation directly
		if !ch && !pg && !qw {
			t.Errorf("catalog relation %q has no physical mapping in any store", name)
		}
	}
}

// And nothing may be mapped that the catalog does not expose.
func TestNoPhysicalMappingWithoutACatalogRelation(t *testing.T) {
	cat := DefaultCatalog()
	for name := range chTables {
		if _, ok := cat.Lookup("", name); !ok {
			t.Errorf("clickhouse mapping %q has no catalog relation", name)
		}
	}
	for name := range pgTables {
		if _, ok := cat.Lookup("", name); !ok {
			t.Errorf("postgres mapping %q has no catalog relation", name)
		}
	}
}

// The tenant must leave as a bound parameter on the wire, not inside the SQL.
func TestClickHouseScannerSendsTenantAsABindParameter(t *testing.T) {
	var gotParam, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotParam = r.URL.Query().Get("param_tenant")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"meta":[{"name":"service_name"}],"data":[["api"]]}`))
	}))
	defer srv.Close()

	rel, _ := DefaultCatalog().Lookup("", "traces")
	s := &ClickHouseScanner{Rel: rel, URL: srv.URL, Client: srv.Client()}
	rows, err := s.Scan(context.Background(), "acme", 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if gotParam != "acme" {
		t.Errorf("param_tenant = %q, want acme", gotParam)
	}
	if strings.Contains(gotBody, "acme") {
		t.Errorf("tenant value was interpolated into the SQL:\n  %s", gotBody)
	}
	if len(rows.Values) != 1 {
		t.Errorf("got %d rows, want 1", len(rows.Values))
	}
}

func TestScannersRefuseAnEmptyTenant(t *testing.T) {
	cat := DefaultCatalog()
	traces, _ := cat.Lookup("", "traces")
	logs, _ := cat.Lookup("", "logs")
	deployments, _ := cat.Lookup("", "deployments")

	scanners := []Scanner{
		&ClickHouseScanner{Rel: traces, URL: "http://ch"},
		&QuickwitScanner{Rel: logs, URL: "http://qw"},
		&PostgresScanner{Rel: deployments},
	}
	for _, s := range scanners {
		if _, err := s.Scan(context.Background(), "  ", 10); err == nil {
			t.Errorf("%T accepted an empty tenant", s)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func logicalNames(cols []chColumn) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.logical
	}
	return out
}

func assertSameColumns(t *testing.T, label string, catalog, mapped []string) {
	t.Helper()
	inCatalog := map[string]bool{}
	for _, c := range catalog {
		inCatalog[c] = true
	}
	inMapped := map[string]bool{}
	for _, c := range mapped {
		inMapped[c] = true
	}
	for c := range inCatalog {
		if !inMapped[c] {
			t.Errorf("%s: catalog exposes %q with no physical mapping", label, c)
		}
	}
	for c := range inMapped {
		if !inCatalog[c] {
			t.Errorf("%s: %q is fetched but absent from the catalog", label, c)
		}
	}
}
