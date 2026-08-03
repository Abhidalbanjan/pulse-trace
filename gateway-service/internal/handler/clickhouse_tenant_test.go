package handler

import "testing"

// The tenant-scope guard is the F0.3 invariant: a ClickHouse read that touches a
// tenant-scoped table without a tenant predicate must be rejected before it ever
// reaches the wire, so a forgotten filter is an error, not a silent cross-tenant
// leak.

func TestAssertTenantScoped(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		{
			name:    "otel_traces with resource-attr tenant clause passes",
			sql:     "SELECT ServiceName FROM pulsetrace.otel_traces WHERE " + tenantClause + " AND Timestamp >= now() - INTERVAL 1 HOUR",
			wantErr: false,
		},
		{
			name:    "otel_traces without any tenant predicate is rejected",
			sql:     "SELECT ServiceName FROM pulsetrace.otel_traces WHERE Timestamp >= now() - INTERVAL 1 HOUR",
			wantErr: true,
		},
		{
			name:    "otel_metrics family (otel_metrics_gauge) is covered by prefix match",
			sql:     "SELECT MetricName FROM pulsetrace.otel_metrics_gauge WHERE TimeUnix >= now()",
			wantErr: true,
		},
		{
			name:    "rum_events with explicit TenantID column passes",
			sql:     "SELECT * FROM pulsetrace.rum_events WHERE TenantID = {tenant:String}",
			wantErr: false,
		},
		{
			name:    "synthetic_results without tenant predicate is rejected",
			sql:     "SELECT * FROM pulsetrace.synthetic_results ORDER BY Timestamp",
			wantErr: true,
		},
		{
			name:    "non-tenant table (system.parts) is not gated",
			sql:     "SELECT count() FROM system.parts WHERE active",
			wantErr: false,
		},
		{
			name:    "tenant bind param alone counts as a predicate",
			sql:     "SELECT * FROM pulsetrace.otel_logs WHERE ServiceName = 'x' AND {tenant:String} = ResourceAttributes['tenant.id']",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := assertTenantScoped(tt.sql)
			if tt.wantErr && err == nil {
				t.Fatalf("expected a tenant-scope error, got nil for: %s", tt.sql)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v for: %s", err, tt.sql)
			}
		})
	}
}

func TestQueryScopedRejectsEmptyTenant(t *testing.T) {
	c := &clickHouseClient{URL: "http://unused.invalid"}
	// Empty tenant must fail closed before any network call.
	if _, err := c.queryScoped("", "SELECT 1 FROM pulsetrace.otel_traces WHERE "+tenantClause, nil); err == nil {
		t.Fatal("queryScoped must reject an empty tenant")
	}
	if _, err := c.queryScoped("   ", "SELECT 1 FROM pulsetrace.otel_traces WHERE "+tenantClause, nil); err == nil {
		t.Fatal("queryScoped must reject a whitespace-only tenant")
	}
}

func TestQueryScopedRejectsUnscopedSQL(t *testing.T) {
	c := &clickHouseClient{URL: "http://unused.invalid"}
	// A real tenant but an unscoped query on a tenant table must still be blocked
	// (this is the case that previously would have leaked across tenants).
	if _, err := c.queryScoped("acme", "SELECT ServiceName FROM pulsetrace.otel_traces", nil); err == nil {
		t.Fatal("queryScoped must reject a tenant-table read with no tenant predicate")
	}
}
