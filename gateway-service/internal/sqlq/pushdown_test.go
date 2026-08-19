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
			"SELECT count(*) FROM logs WHERE level != 'error'",
			"only equality is translated; an inequality is left to local execution",
		},
		{
			"SELECT count(*) FROM logs WHERE level = 'error' OR level = 'warn'",
			"OR is not a conjunction of equality tests",
		},
		{
			"SELECT count(*) FROM logs WHERE level = 'error' AND message LIKE '%x%'",
			"one translatable conjunct does not make the whole clause translatable",
		},
		{
			"SELECT count(*) FROM logs WHERE status_code = 500",
			"a numeric literal is bound against a column whose physical type this package does not track",
		},
		{
			"SELECT count(*) FROM logs WHERE nonexistent = 'x'",
			"a filter column the catalog does not expose must not reach a store",
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

// ── Filtered push-down ───────────────────────────────────────────────────────

// The shapes that must translate, and the predicates they must carry.
func TestFilteredAggregatesPushDown(t *testing.T) {
	cases := []struct {
		sql   string
		kind  pushdownKind
		where []Predicate
	}{
		{
			"SELECT count(*) FROM logs WHERE level = 'ERROR'",
			pushCountAll,
			[]Predicate{{Column: "level", Value: "ERROR"}},
		},
		{
			// Operand order must not matter; SQL does not care and neither
			// should the matcher.
			"SELECT count(*) FROM logs WHERE 'ERROR' = level",
			pushCountAll,
			[]Predicate{{Column: "level", Value: "ERROR"}},
		},
		{
			"SELECT count(*) FROM logs WHERE level = 'ERROR' AND service_name = 'checkout'",
			pushCountAll,
			[]Predicate{{Column: "level", Value: "ERROR"}, {Column: "service_name", Value: "checkout"}},
		},
		{
			"SELECT count(*) FROM logs WHERE (level = 'ERROR') AND (service_name = 'checkout')",
			pushCountAll,
			[]Predicate{{Column: "level", Value: "ERROR"}, {Column: "service_name", Value: "checkout"}},
		},
		{
			// The benchmark's high-cardinality class, which is the reason this
			// exists: group by an open-ended attribute, filtered by level.
			"SELECT `metadata.customer_id`, count(*) AS n FROM logs WHERE level = 'ERROR' GROUP BY `metadata.customer_id` ORDER BY n DESC LIMIT 20",
			pushGroupCount,
			[]Predicate{{Column: "level", Value: "ERROR"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			p := plan(t, tc.sql)
			if p == nil {
				t.Fatalf("expected push-down, got local execution")
			}
			if p.kind != tc.kind {
				t.Fatalf("kind = %v, want %v", p.kind, tc.kind)
			}
			if len(p.where) != len(tc.where) {
				t.Fatalf("predicates = %+v, want %+v", p.where, tc.where)
			}
			for i, want := range tc.where {
				if p.where[i] != want {
					t.Errorf("predicate %d = %+v, want %+v", i, p.where[i], want)
				}
			}
		})
	}
}

// Every predicate handed to a store must name a column the relation exposes.
// This is the same guarantee TestPushedGroupColumnIsAlwaysACatalogColumn makes
// for the grouped column, extended to the filter — a filter column is equally
// a name that becomes part of a store request.
func TestPushedFilterColumnsAreAlwaysCatalogColumns(t *testing.T) {
	cat := DefaultCatalog()
	for _, sql := range []string{
		"SELECT count(*) FROM logs WHERE level = 'ERROR'",
		"SELECT count(*) FROM logs WHERE `metadata.customer_id` = 'cust-1'",
		"SELECT status, count(*) AS n FROM incidents WHERE severity = 'SEV1' GROUP BY status",
		"SELECT count(*) FROM traces WHERE service_name = 'checkout'",
	} {
		p := plan(t, sql)
		if p == nil {
			t.Fatalf("expected push-down for %s", sql)
		}
		rel, _ := cat.Lookup("", p.rel.Name)
		for _, pred := range p.where {
			if !rel.HasColumn(pred.Column) {
				t.Errorf("%s: would send filter column %q, which %s does not expose",
					sql, pred.Column, rel.Name)
			}
		}
	}
}

// An attribute key keeps the customer's spelling all the way to the store.
//
// Regression: columnNameOf returned the parser's lowercased name, so
// `metadata.customerId` addressed `metadata.customerid`. Quickwit field names
// are case-sensitive, so that resolved, pushed down, and returned zero buckets
// — a wrong answer that looked like a real one. Declared columns are matched
// case-insensitively and must stay that way.
func TestAttributeKeysKeepTheirCase(t *testing.T) {
	p := plan(t, "SELECT `metadata.customerId`, count(*) AS n FROM logs GROUP BY `metadata.customerId`")
	if p == nil {
		t.Fatal("expected push-down")
	}
	if p.column != "metadata.customerId" {
		t.Errorf("column reaching the store = %q, want the original spelling", p.column)
	}

	// Declared columns remain case-insensitive: SQL identifiers are, and the
	// physical mapping is lowercase.
	for _, sql := range []string{
		"SELECT count(*) FROM logs WHERE Level = 'ERROR'",
		"SELECT SERVICE_NAME, count(*) AS n FROM logs GROUP BY SERVICE_NAME",
	} {
		if plan(t, sql) == nil {
			t.Errorf("case-insensitive declared column did not push down: %s", sql)
		}
	}
}

// An attribute key that could become syntax in a Quickwit query string must not
// resolve as a column at all.
func TestAttributeKeysAreShapeChecked(t *testing.T) {
	rel, _ := DefaultCatalog().Lookup("", "logs")
	for _, bad := range []string{
		"metadata.a b",            // clause separator
		"metadata.a:b",            // field separator
		"metadata.a\"b",           // quote
		"metadata.",               // empty key
		"metadata.a.b",            // deeper nesting is not addressed here
		"metadata.1a",             // must start identifier-like
		"metadata." + str64plus(), // longer than the pattern permits
		"notmetadata.customer_id", // a prefix this relation does not declare
	} {
		if rel.HasColumn(bad) {
			t.Errorf("accepted an attribute name that could alter a query: %q", bad)
		}
	}
	for _, good := range []string{"metadata.customer_id", "metadata.pod", "metadata._internal", "metadata.customerId"} {
		if !rel.HasColumn(good) {
			t.Errorf("rejected a legitimate attribute name: %q", good)
		}
	}
	// A relation that declares no prefix has no attributes at all.
	inc, _ := DefaultCatalog().Lookup("", "incidents")
	if inc.HasColumn("metadata.customer_id") {
		t.Error("incidents exposes attributes but declares no prefix")
	}
}

func str64plus() string {
	s := ""
	for len(s) < 65 {
		s += "a"
	}
	return s
}
