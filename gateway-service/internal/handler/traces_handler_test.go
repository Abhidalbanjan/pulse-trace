package handler

import (
	"strings"
	"testing"
)

func TestParseTagFilters(t *testing.T) {
	got := parseTagFilters([]string{
		"http.method:GET",
		"http.url:https://x.com/a:b", // value may contain ':'
		"malformed",                  // no separator
		":novalue",                   // empty key
		"nokey:",                     // empty value
		"  http.status:500  ",        // trimmed
	})
	want := []traceTag{
		{Key: "http.method", Value: "GET"},
		{Key: "http.url", Value: "https://x.com/a:b"},
		{Key: "http.status", Value: "500"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d tags, got %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestBuildTraceSearchSQL_AlwaysTenantScoped(t *testing.T) {
	sql, _ := buildTraceSearchSQL(traceFilters{Interval: "1 HOUR", Limit: 100})
	if !strings.Contains(sql, "ResourceAttributes['tenant.id'] = {tenant:String}") {
		t.Fatal("every trace search must carry the tenant predicate")
	}
	if !strings.Contains(sql, "GROUP BY TraceId") || !strings.Contains(sql, "FORMAT JSON") {
		t.Fatal("expected grouped JSON query")
	}
}

func TestBuildTraceSearchSQL_FiltersBecomeBindParams(t *testing.T) {
	f := traceFilters{
		Service:   "payment-service",
		Operation: "POST /checkout",
		Status:    "error",
		MinMs:     100, HasMin: true,
		MaxMs: 2000, HasMax: true,
		Tags:     []traceTag{{Key: "http.method", Value: "POST"}},
		Interval: "1 HOUR",
		Limit:    50,
	}
	sql, params := buildTraceSearchSQL(f)

	// Each user value is bound, and its raw literal never appears inline in SQL.
	checks := map[string]string{
		"svc": "payment-service",
		"op":  "POST /checkout",
		"tk0": "http.method",
		"tv0": "POST",
	}
	for p, v := range checks {
		if params[p] != v {
			t.Errorf("param %q = %q, want %q", p, params[p], v)
		}
		if strings.Contains(sql, v) {
			t.Errorf("user value %q must be a bind param, not inlined in SQL", v)
		}
	}
	for _, frag := range []string{
		"root_service = {svc:String}",
		"root_operation = {op:String}",
		"duration_ms >= {minms:Float64}",
		"duration_ms <= {maxms:Float64}",
		"error_count > 0",
		"countIf(SpanAttributes[{tk0:String}] = {tv0:String}) > 0",
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("expected SQL to contain %q", frag)
		}
	}
	// Limit is a validated int, inlined.
	if !strings.Contains(sql, "LIMIT 50") {
		t.Error("expected LIMIT 50 inlined")
	}
}

func TestBuildTraceSearchSQL_StatusOk(t *testing.T) {
	sql, _ := buildTraceSearchSQL(traceFilters{Status: "ok", Interval: "1 HOUR", Limit: 10})
	if !strings.Contains(sql, "error_count = 0") {
		t.Fatal("status=ok should require zero errors")
	}
}

func TestBuildTraceSearchSQL_NoFiltersNoHaving(t *testing.T) {
	sql, params := buildTraceSearchSQL(traceFilters{Interval: "1 HOUR", Limit: 100})
	if strings.Contains(sql, "HAVING") {
		t.Fatal("no filters should produce no HAVING clause")
	}
	if len(params) != 0 {
		t.Fatalf("no filters should bind no params, got %v", params)
	}
}

func TestBuildTraceSpansSQL_TenantScopedAndBound(t *testing.T) {
	sql, _ := buildTraceSpansSQL()
	if !strings.Contains(sql, "ResourceAttributes['tenant.id'] = {tenant:String}") {
		t.Fatal("span fetch must be tenant-scoped")
	}
	if !strings.Contains(sql, "TraceId = {traceId:String}") {
		t.Fatal("trace id must be a bind param, not concatenated")
	}
	if !strings.Contains(sql, "ORDER BY Timestamp ASC") {
		t.Fatal("spans should be oldest-first for the waterfall")
	}
}
