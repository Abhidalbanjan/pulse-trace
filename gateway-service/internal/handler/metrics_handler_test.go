package handler

import (
	"strings"
	"testing"
)

func TestMetricAggExpr_SupportedFunctions(t *testing.T) {
	cases := []struct {
		fn            string
		bucketSeconds int
		wantContains  string
	}{
		{"", 60, "avg(Value)"},   // default
		{"avg", 60, "avg(Value)"},
		{"min", 60, "min(Value)"},
		{"max", 60, "max(Value)"},
		{"sum", 60, "sum(Value)"},
		{"p50", 60, "quantile(0.50)(Value)"},
		{"p90", 60, "quantile(0.90)(Value)"},
		{"p95", 60, "quantile(0.95)(Value)"},
		{"p99", 60, "quantile(0.99)(Value)"},
	}
	for _, c := range cases {
		expr, ok := metricAggExpr(c.fn, c.bucketSeconds)
		if !ok {
			t.Errorf("fn %q should be supported", c.fn)
			continue
		}
		if !strings.Contains(expr, c.wantContains) {
			t.Errorf("fn %q expr = %q, want to contain %q", c.fn, expr, c.wantContains)
		}
	}
}

func TestMetricAggExpr_RateUsesBucketWidth(t *testing.T) {
	// rate() must divide the per-bucket counter increase by the bucket width in
	// seconds, and must not go negative on a reset.
	expr, ok := metricAggExpr("rate", 900)
	if !ok {
		t.Fatal("rate should be supported with a positive bucket width")
	}
	if !strings.Contains(expr, "max(Value) - min(Value)") {
		t.Errorf("rate should be based on the counter increase, got %q", expr)
	}
	if !strings.Contains(expr, "/ 900") {
		t.Errorf("rate should divide by the bucket width (900s), got %q", expr)
	}
	if !strings.Contains(expr, "greatest(") {
		t.Errorf("rate should clamp resets to >= 0, got %q", expr)
	}
}

func TestMetricAggExpr_RateRequiresPositiveBucket(t *testing.T) {
	// A zero/negative bucket width would divide by zero — reject it rather than
	// emit invalid SQL.
	if _, ok := metricAggExpr("rate", 0); ok {
		t.Error("rate with a non-positive bucket width must be rejected")
	}
}

func TestMetricAggExpr_UnknownRejected(t *testing.T) {
	// An unknown function must be rejected, never silently coerced to avg.
	for _, bad := range []string{"median", "stddev", "p999", "count", "drop table"} {
		if _, ok := metricAggExpr(bad, 60); ok {
			t.Errorf("fn %q must be rejected", bad)
		}
	}
}

func TestMetricIntervalBucketSeconds_MatchesBuckets(t *testing.T) {
	// Every interval with a bucket expression must have a matching bucket-width
	// (rate() depends on the two staying in lock-step).
	for interval := range metricIntervalToBucket {
		if _, ok := metricIntervalBucketSeconds[interval]; !ok {
			t.Errorf("interval %q has a bucket expression but no bucket-width for rate()", interval)
		}
	}
}

func TestResolveMetricTable(t *testing.T) {
	if resolveMetricTable("sum") != "otel_metrics_sum" {
		t.Error("sum should resolve to otel_metrics_sum")
	}
	if resolveMetricTable("gauge") != "otel_metrics_gauge" {
		t.Error("gauge should resolve to otel_metrics_gauge")
	}
	// Unknown/empty type defaults to gauge (never an unvalidated table name).
	if resolveMetricTable("") != "otel_metrics_gauge" || resolveMetricTable("bogus; DROP") != "otel_metrics_gauge" {
		t.Error("unknown type must default to the gauge table")
	}
}

func TestBuildMetricCatalogSQL_TenantScopedBoundAndTableFromEnum(t *testing.T) {
	sql, params := buildMetricCatalogSQL(resolveMetricTable("sum"))
	if !strings.Contains(sql, "ResourceAttributes['tenant.id'] = {tenant:String}") {
		t.Fatal("catalog query must be tenant-scoped")
	}
	if !strings.Contains(sql, "MetricName = {metric:String}") {
		t.Fatal("metric name must be a bind param, not concatenated")
	}
	if !strings.Contains(sql, "pulsetrace.otel_metrics_sum") {
		t.Fatal("expected the resolved (enum) table in the FROM clause")
	}
	if !strings.Contains(sql, "arrayJoin(Attributes)") || !strings.Contains(sql, "GROUP BY label_key, label_value") {
		t.Fatal("expected label-facet extraction over the Attributes map")
	}
	// The builder binds nothing itself; the handler adds metric + tenant.
	if len(params) != 0 {
		t.Fatalf("builder should return no params, got %v", params)
	}
}
