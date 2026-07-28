package handler

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildTraceAnalyticsWhere_TenantAndRootScopeAlwaysPresent(t *testing.T) {
	where, params, err := buildTraceAnalyticsWhere(url.Values{}, "acme", "1 HOUR")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Tenant isolation and the root-span restriction are invariants, not options.
	if !strings.Contains(where, tenantClause) {
		t.Errorf("where missing tenant scope: %q", where)
	}
	if !strings.Contains(where, "ParentSpanId = ''") {
		t.Errorf("where missing root-span restriction: %q", where)
	}
	if params["tenant"] != "acme" {
		t.Errorf("tenant param = %q, want acme", params["tenant"])
	}
}

func TestBuildTraceAnalyticsWhere_RegexFiltersAreBound(t *testing.T) {
	q := url.Values{
		"route_regex":     {"/api/v[0-9]+/.*"},
		"operation_regex": {"GET .*"},
	}
	where, params, err := buildTraceAnalyticsWhere(q, "t1", "24 HOUR")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The regex must go through a bind param, never be concatenated into the SQL.
	if !strings.Contains(where, "match(SpanAttributes['http.route'], {route_regex:String})") {
		t.Errorf("route regex not bound as a param: %q", where)
	}
	if !strings.Contains(where, "match(SpanName, {operation_regex:String})") {
		t.Errorf("operation regex not bound as a param: %q", where)
	}
	if params["route_regex"] != "/api/v[0-9]+/.*" || params["operation_regex"] != "GET .*" {
		t.Errorf("regex params not passed through verbatim: %+v", params)
	}
	if strings.Contains(where, "/api/v[0-9]+") {
		t.Errorf("regex pattern must not be inlined into the SQL: %q", where)
	}
}

func TestBuildTraceAnalyticsWhere_InvalidRegexRejected(t *testing.T) {
	for _, field := range []string{"route_regex", "operation_regex"} {
		q := url.Values{field: {"("}} // unbalanced group
		if _, _, err := buildTraceAnalyticsWhere(q, "t1", "1 HOUR"); err == nil {
			t.Errorf("expected an error for an invalid %s", field)
		}
	}
}

func TestBuildTraceAnalyticsWhere_ExactFilters(t *testing.T) {
	q := url.Values{"service": {"api", "web"}, "route": {"/checkout"}}
	where, params, err := buildTraceAnalyticsWhere(q, "t1", "1 HOUR")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(where, "ServiceName IN {services:Array(String)}") {
		t.Errorf("service IN-list missing: %q", where)
	}
	if !strings.Contains(where, "SpanAttributes['http.route'] IN {routes:Array(String)}") {
		t.Errorf("route IN-list missing: %q", where)
	}
	if params["services"] == "" || params["routes"] == "" {
		t.Errorf("array params not set: %+v", params)
	}
}
