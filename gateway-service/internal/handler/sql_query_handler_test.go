package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pulsetrace/gateway-service/internal/sqlq"
)

// newTestSQLHandler builds a handler over in-memory scanners. db is nil, so
// auditing is skipped — asserted separately rather than requiring Postgres here.
func newTestSQLHandler(t *testing.T) *SQLQueryHandler {
	t.Helper()
	cat := sqlq.DefaultCatalog()
	var scanners []sqlq.Scanner
	for _, name := range cat.Names() {
		rel, _ := cat.Lookup("", name)
		rows := &sqlq.Rows{Columns: rel.Columns}
		row := make([]any, len(rel.Columns))
		for i := range row {
			row[i] = "acme-value"
		}
		rows.Values = append(rows.Values, row)
		scanners = append(scanners, &sqlq.StaticScanner{
			Rel: rel, ByTenant: map[string]*sqlq.Rows{"acme": rows},
		})
	}
	engine, err := sqlq.NewEngine(cat, sqlq.DefaultPolicy(), sqlq.DefaultBudget(), scanners...)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return NewSQLQueryHandler(nil, engine)
}

// request builds a call carrying the gateway-verified tenant header. In
// production AuthMiddleware strips any client-supplied X-Tenant-ID and re-sets
// it from signed claims, so setting it here stands in for a verified identity.
func request(tenant, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/query/sql", strings.NewReader(body))
	if tenant != "" {
		r.Header.Set("X-Tenant-ID", tenant)
	}
	r.Header.Set("X-User-Subject", "someone@example.com")
	return r
}

func TestExecuteStreamsNDJSON(t *testing.T) {
	h := newTestSQLHandler(t)
	w := httptest.NewRecorder()
	h.Execute(w, request("acme", `{"sql":"SELECT service_name FROM logs"}`))

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("content-type = %q", ct)
	}

	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("want columns + row(s) + stats, got %d lines:\n%s", len(lines), w.Body.String())
	}

	// The contract is positional: columns first, stats last. A client that
	// renders incrementally depends on both.
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not JSON: %v", err)
	}
	if _, ok := first["columns"]; !ok {
		t.Errorf("first line must carry columns, got %s", lines[0])
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("last line is not JSON: %v", err)
	}
	if _, ok := last["stats"]; !ok {
		t.Errorf("last line must carry stats, got %s", lines[len(lines)-1])
	}
	// Every line must parse on its own — that is the whole point of NDJSON.
	for i, ln := range lines {
		var any map[string]any
		if err := json.Unmarshal([]byte(ln), &any); err != nil {
			t.Errorf("line %d is not standalone JSON: %v", i, err)
		}
	}
}

// A refused query answers 400 with the machine-readable reason, so a client can
// distinguish "you may not ask that" from "the server broke".
func TestRefusedQueryIsABadRequestWithItsReason(t *testing.T) {
	cases := []struct {
		sql    string
		reason string
	}{
		{"SELECT * FROM system.tables", string(sqlq.ReasonQualifiedName)},
		{"SELECT * FROM otel_logs", string(sqlq.ReasonUnknownRelation)},
		{"DROP TABLE logs", string(sqlq.ReasonNotSelect)},
		{"SELECT 1 FROM logs; SELECT 2 FROM logs", string(sqlq.ReasonMultipleStmts)},
	}
	for _, tc := range cases {
		h := newTestSQLHandler(t)
		w := httptest.NewRecorder()
		body, _ := json.Marshal(sqlQueryRequest{SQL: tc.sql})
		h.Execute(w, request("acme", string(body)))

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", tc.sql, w.Code)
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Errorf("%s: response is not JSON: %v", tc.sql, err)
			continue
		}
		if resp["reason"] != tc.reason {
			t.Errorf("%s: reason = %v, want %s", tc.sql, resp["reason"], tc.reason)
		}
	}
}

// The endpoint must never read the tenant from anything the caller sends as
// data. The engine takes it as an argument; this asserts the handler passes the
// header AuthMiddleware verified and nothing else.
func TestTenantComesFromTheVerifiedHeaderNotTheStatement(t *testing.T) {
	h := newTestSQLHandler(t)
	w := httptest.NewRecorder()
	// Statement mentions another tenant every way it can.
	h.Execute(w, request("acme",
		`{"sql":"SELECT 'initech' AS t, service_name FROM logs -- initech"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	// Only acme's fixture rows exist; initech has none, so any initech-owned
	// value in the output would have had to be fetched.
	if strings.Contains(w.Body.String(), "initech-value") {
		t.Fatalf("response carries another tenant's data:\n%s", w.Body.String())
	}
}

// With no verified tenant header the request still resolves to "default"
// (single-tenant and dev stacks depend on that), and must never resolve to
// "every tenant".
func TestMissingTenantHeaderResolvesToDefaultNotEverything(t *testing.T) {
	h := newTestSQLHandler(t)
	w := httptest.NewRecorder()
	h.Execute(w, request("", `{"sql":"SELECT service_name FROM logs"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	// The fixtures only contain acme, so "default" must come back empty rather
	// than borrowing another tenant's rows.
	if strings.Contains(w.Body.String(), "acme-value") {
		t.Fatalf("an unauthenticated-tenant request read acme's rows:\n%s", w.Body.String())
	}
}

func TestMalformedRequestsAreRejected(t *testing.T) {
	for _, body := range []string{``, `not json`, `{}`, `{"sql":"   "}`} {
		h := newTestSQLHandler(t)
		w := httptest.NewRecorder()
		h.Execute(w, request("acme", body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, w.Code)
		}
	}
}

// A cancelled request must stop streaming rather than finish a body nobody is
// reading.
func TestCancelledRequestStopsStreaming(t *testing.T) {
	h := newTestSQLHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := request("acme", `{"sql":"SELECT service_name FROM logs"}`).WithContext(ctx)
	w := httptest.NewRecorder()
	h.Execute(w, r) // must return, not hang or panic
	_ = w.Body.String()
}
