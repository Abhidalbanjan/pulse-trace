package sqlq

import (
	"context"
	"strings"

	"github.com/pingcap/tidb/pkg/parser/ast"
)

// Aggregation push-down.
//
// # Why this exists
//
// Executing locally is what makes cross-tenant access unrepresentable, but it
// has a hard cost: every row an aggregate covers has to be fetched first.
// Measured against the real stack, `SELECT count(*) FROM logs` over the 5.1M
// benchmark corpus could not be answered at all — Quickwit caps a search at
// `max_hits = 10_000`, so the class was expressible and still not answerable.
// That is dimension D3, and shipping "you can write it, it just always fails"
// would not have closed it.
//
// The stores can already do this work. Quickwit returns an exact `num_hits`
// for a search with `max_hits: 0` — 5,104,773 rows counted in 48 ms — and
// supports terms aggregations for GROUP BY. ClickHouse and Postgres are SQL.
// So for the shapes below the aggregate is computed *in the store* and only the
// answer travels.
//
// # Why this does not weaken the isolation argument
//
// Push-down never sends the user's SQL anywhere. The engine recognises a shape
// in the validated AST and then calls a *typed method* — CountAll, GroupCount —
// on the scanner, which builds its own statement exactly as it does for a row
// scan, with the tenant bound the same way. The user controls which shape is
// matched and which catalog column is grouped on; they never contribute syntax.
// A column name is checked against the catalog before it is used, so it cannot
// name anything the relation does not expose.

// Aggregator is an optional Scanner capability. A store that can answer an
// aggregate itself implements it; one that cannot simply does not, and the
// engine falls back to fetching rows.
type Aggregator interface {
	// CountAll returns the exact number of rows for the tenant.
	CountAll(ctx context.Context, tenantID string) (int64, error)
	// GroupCount returns (value, count) pairs for one column, ordered by count
	// descending, at most limit groups.
	GroupCount(ctx context.Context, tenantID, column string, limit int) (*Rows, error)
}

// pushdownKind names a recognised aggregate shape.
type pushdownKind int

const (
	pushNone pushdownKind = iota
	pushCountAll
	pushGroupCount
)

// pushdownPlan describes an aggregate the store can answer directly.
type pushdownPlan struct {
	kind pushdownKind
	rel  Relation
	// column is the GROUP BY column, for pushGroupCount.
	column string
	// countAlias / groupAlias are the output column names the user asked for,
	// so the result matches the statement they wrote rather than our internals.
	groupAlias string
	countAlias string
	limit      int
}

// defaultGroupLimit bounds a GROUP BY that carried no LIMIT.
//
// An unbounded terms aggregation over a high-cardinality column is a way to ask
// a store for millions of buckets, which is the same denial-of-service the row
// budget exists to prevent — the fact that the work happens remotely does not
// make it free.
const defaultGroupLimit = 1000

// planPushdown recognises the two aggregate shapes worth pushing down, and
// refuses everything else.
//
// Deliberately narrow. Each shape admitted here is one whose translation is
// provably faithful; a general SQL-to-store compiler would be a second query
// engine with its own semantics, and a subtle mistranslation returns a wrong
// number rather than an error.
func planPushdown(a *Analysis) *pushdownPlan {
	sel, ok := a.Stmt.(*ast.SelectStmt)
	if !ok || len(a.Relations) != 1 {
		return nil
	}
	rel := a.Relations[0]

	// Anything that filters, joins, or post-processes changes the answer, and
	// none of it is translated here.
	if sel.Where != nil || sel.Having != nil || sel.Distinct ||
		sel.WindowSpecs != nil || sel.With != nil || sel.LockInfo != nil {
		return nil
	}
	if !singleTableFrom(sel) {
		return nil
	}

	fields := sel.Fields.Fields
	switch {
	// SELECT count(*) FROM rel
	case len(fields) == 1 && sel.GroupBy == nil:
		alias, ok := countStar(fields[0])
		if !ok {
			return nil
		}
		if sel.OrderBy != nil || sel.Limit != nil {
			// Ordering or limiting a single row is meaningless but harmless;
			// refusing keeps the translated set to exactly what was verified.
			return nil
		}
		return &pushdownPlan{kind: pushCountAll, rel: rel, countAlias: alias}

	// SELECT col, count(*) FROM rel GROUP BY col [ORDER BY count DESC] [LIMIT n]
	case len(fields) == 2 && sel.GroupBy != nil && len(sel.GroupBy.Items) == 1:
		colName, groupAlias, ok := plainColumn(fields[0])
		if !ok {
			return nil
		}
		countAlias, ok := countStar(fields[1])
		if !ok {
			return nil
		}
		byName, ok := columnNameOf(sel.GroupBy.Items[0].Expr)
		if !ok || !strings.EqualFold(byName, colName) {
			return nil
		}
		if !rel.HasColumn(colName) {
			// Cannot happen for a validated statement, but the column is about
			// to be handed to a store; check it against the catalog rather than
			// trust that it was checked earlier.
			return nil
		}
		// Only "most frequent first" is translated. A different ordering would
		// need the full bucket set to sort correctly, which is the thing being
		// avoided.
		if sel.OrderBy != nil && !ordersByCountDesc(sel.OrderBy, countAlias) {
			return nil
		}
		limit := defaultGroupLimit
		if sel.Limit != nil {
			n, ok := limitCount(sel.Limit)
			if !ok {
				return nil
			}
			limit = n
		}
		return &pushdownPlan{
			kind: pushGroupCount, rel: rel, column: colName,
			groupAlias: groupAlias, countAlias: countAlias, limit: limit,
		}
	}
	return nil
}

// singleTableFrom reports whether FROM names exactly one table and no join.
func singleTableFrom(sel *ast.SelectStmt) bool {
	if sel.From == nil || sel.From.TableRefs == nil {
		return false
	}
	j := sel.From.TableRefs
	if j.Right != nil {
		return false
	}
	src, ok := j.Left.(*ast.TableSource)
	if !ok {
		return false
	}
	_, ok = src.Source.(*ast.TableName)
	return ok
}

// countStar matches `count(*)`, returning the output name.
func countStar(f *ast.SelectField) (string, bool) {
	agg, ok := f.Expr.(*ast.AggregateFuncExpr)
	if !ok || !strings.EqualFold(agg.F, "count") || agg.Distinct || len(agg.Args) != 1 {
		return "", false
	}
	// count(*) parses as count(1); count(col) does not push down here because
	// it does not count NULLs the same way.
	if _, isValue := agg.Args[0].(ast.ValueExpr); !isValue {
		return "", false
	}
	name := f.AsName.O
	if name == "" {
		name = "count(*)"
	}
	return name, true
}

// plainColumn matches a bare column reference, returning its name and output name.
func plainColumn(f *ast.SelectField) (col string, alias string, ok bool) {
	name, ok := columnNameOf(f.Expr)
	if !ok {
		return "", "", false
	}
	alias = f.AsName.O
	if alias == "" {
		alias = name
	}
	return name, alias, true
}

func columnNameOf(e ast.ExprNode) (string, bool) {
	c, ok := e.(*ast.ColumnNameExpr)
	if !ok || c.Name == nil {
		return "", false
	}
	// A qualified reference would name a table; the single-relation check above
	// makes that redundant, but resolving it here would be guessing.
	if c.Name.Table.L != "" || c.Name.Schema.L != "" {
		return "", false
	}
	return c.Name.Name.L, true
}

func ordersByCountDesc(o *ast.OrderByClause, countAlias string) bool {
	if len(o.Items) != 1 || !o.Items[0].Desc {
		return false
	}
	// Either `ORDER BY n DESC` (the alias) or `ORDER BY count(*) DESC`.
	if name, ok := columnNameOf(o.Items[0].Expr); ok {
		return strings.EqualFold(name, countAlias)
	}
	agg, ok := o.Items[0].Expr.(*ast.AggregateFuncExpr)
	return ok && strings.EqualFold(agg.F, "count")
}

func limitCount(l *ast.Limit) (int, bool) {
	if l.Offset != nil || l.Count == nil {
		return 0, false // OFFSET needs the skipped buckets, so it is not pushed
	}
	v, ok := l.Count.(ast.ValueExpr)
	if !ok {
		return 0, false
	}
	n, ok := v.GetValue().(uint64)
	// A limit of zero asks for nothing; an absurd one asks the store for more
	// buckets than the local path would have been allowed to fetch, which would
	// make push-down the cheaper way to do the expensive thing.
	if !ok || n == 0 || n > uint64(defaultGroupLimit)*100 {
		return 0, false
	}
	return int(n), true
}
