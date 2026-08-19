package sqlq

import "testing"

// Push-down is the one place in this package where a bug returns a *wrong
// answer* rather than an error. Everything else fails closed: a bad statement
// is refused, a bad scanner errors. A mistranslated aggregate returns a number
// that looks exactly like a right one.
//
// So the matcher is tested from both sides. Shapes that must push down, because
// otherwise the query cannot be answered at all at corpus scale; and shapes
// that must NOT, because their translation is not obviously faithful and a
// plausible wrong number is worse than a refusal.

func plan(t *testing.T, sql string) *pushdownPlan {
	t.Helper()
	a, err := Validate(sql, DefaultCatalog(), DefaultPolicy())
	if err != nil {
		t.Fatalf("statement did not validate: %s\n  %v", sql, err)
	}
	return planPushdown(a)
}

func TestShapesThatMustPushDown(t *testing.T) {
	cases := []struct {
		sql        string
		kind       pushdownKind
		column     string
		countAlias string
		limit      int
	}{
		{"SELECT count(*) FROM logs", pushCountAll, "", "count(*)", 0},
		{"SELECT count(*) AS n FROM logs", pushCountAll, "", "n", 0},
		{"SELECT count(*) AS total FROM traces", pushCountAll, "", "total", 0},
		{
			"SELECT service_name, count(*) AS n FROM logs GROUP BY service_name ORDER BY n DESC LIMIT 10",
			pushGroupCount, "service_name", "n", 10,
		},
		{
			// No LIMIT: bounded by the default rather than refused, since an
			// unbounded terms aggregation is the store-side version of the
			// unbounded fetch the row budget exists to prevent.
			"SELECT level, count(*) AS n FROM logs GROUP BY level",
			pushGroupCount, "level", "n", defaultGroupLimit,
		},
		{
			// Ordering by the aggregate itself rather than its alias.
			"SELECT service_name, count(*) FROM logs GROUP BY service_name ORDER BY count(*) DESC LIMIT 5",
			pushGroupCount, "service_name", "count(*)", 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			p := plan(t, tc.sql)
			if p == nil {
				t.Fatalf("did not push down; at corpus scale this query cannot be answered locally")
			}
			if p.kind != tc.kind {
				t.Errorf("kind = %v, want %v", p.kind, tc.kind)
			}
			if p.column != tc.column {
				t.Errorf("column = %q, want %q", p.column, tc.column)
			}
			if p.countAlias != tc.countAlias {
				t.Errorf("countAlias = %q, want %q", p.countAlias, tc.countAlias)
			}
			if tc.limit != 0 && p.limit != tc.limit {
				t.Errorf("limit = %d, want %d", p.limit, tc.limit)
			}
		})
	}
}

// Each of these would change the answer if translated naively, so the matcher
// must decline and let local execution handle it.
func TestShapesThatMustNotPushDown(t *testing.T) {
	cases := []struct{ sql, why string }{
		{
			"SELECT count(*) FROM logs WHERE level = 'error'",
			"a WHERE clause is not translated; pushing it down as an unfiltered count would overcount",
		},
		{
			"SELECT service_name, count(*) AS n FROM logs GROUP BY service_name HAVING count(*) > 5",
			"HAVING filters buckets after grouping",
		},
		{
			"SELECT count(*) FROM logs UNION ALL SELECT count(*) FROM traces",
			"a set operation is not a single-relation aggregate",
		},
		{
			"SELECT l.service_name, count(*) FROM logs l JOIN traces t ON l.trace_id = t.trace_id GROUP BY l.service_name",
			"a join needs both relations locally",
		},
		{
			"SELECT count(DISTINCT service_name) FROM logs",
			"count(distinct) is a different aggregate than count(*)",
		},
		{
			"SELECT count(service_name) FROM logs",
			"count(col) skips NULLs; count(*) does not",
		},
		{
			"SELECT service_name, count(*) AS n FROM logs GROUP BY service_name ORDER BY service_name",
			"ordering by the grouped column needs every bucket, not the top ones",
		},
		{
			"SELECT service_name, count(*) AS n FROM logs GROUP BY service_name ORDER BY n ASC LIMIT 10",
			"ascending order asks for the rarest buckets, which a top-N terms aggregation does not return",
		},
		{
			"SELECT service_name, count(*) AS n FROM logs GROUP BY service_name ORDER BY n DESC LIMIT 10 OFFSET 5",
			"OFFSET needs the buckets that were skipped",
		},
		{
			"SELECT service_name, level, count(*) FROM logs GROUP BY service_name",
			"a projection that is neither the grouped column nor the count",
		},
		{
			"SELECT count(*) AS n FROM logs LIMIT 1",
			"only the exact verified shapes are translated",
		},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			if p := plan(t, tc.sql); p != nil {
				t.Fatalf("pushed down (%v) but must not: %s", p.kind, tc.why)
			}
		})
	}
}

// A pushed-down group-by names a column to the store. It must be one the
// catalog exposes, checked against the relation rather than trusted from the
// statement.
func TestPushedGroupColumnIsAlwaysACatalogColumn(t *testing.T) {
	cat := DefaultCatalog()
	for _, sql := range []string{
		"SELECT service_name, count(*) AS n FROM logs GROUP BY service_name",
		"SELECT level, count(*) AS n FROM logs GROUP BY level",
		"SELECT status, count(*) AS n FROM incidents GROUP BY status",
		"SELECT check_name, count(*) AS n FROM synthetic_results GROUP BY check_name",
	} {
		p := plan(t, sql)
		if p == nil {
			t.Fatalf("expected push-down for %s", sql)
		}
		rel, _ := cat.Lookup("", p.rel.Name)
		if !rel.HasColumn(p.column) {
			t.Errorf("%s: would send column %q, which %s does not expose", sql, p.column, rel.Name)
		}
	}
}
