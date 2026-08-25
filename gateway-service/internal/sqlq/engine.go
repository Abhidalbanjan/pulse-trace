package sqlq

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2" // local execution engine
	"github.com/pingcap/tidb/pkg/parser/format"
)

// Engine runs validated user SQL against tenant-bound data.
//
// # The isolation argument, in one place
//
// A query is executed in four steps, and the ordering is the whole point:
//
//  1. Validate. The statement may name only catalog relations (policy.go).
//  2. Scan. For each named relation, a Scanner fetches rows for the tenant on
//     the authenticated request. The user's SQL is not passed to any scanner
//     and cannot reach any store.
//  3. Load. Those rows — already single-tenant — go into a private, in-memory
//     DuckDB that exists for the lifetime of this one query.
//  4. Execute. The user's statement runs there, against nothing else.
//
// Cross-tenant access is therefore not "blocked": it is unrepresentable. There
// is no statement whose acceptance would cause another tenant's rows to be
// fetched, because fetching happens before the statement runs and takes no
// input from it. A parser bug, a policy gap or a novel injection technique
// costs an attacker access to their own data, which they already had.
//
// What this does not defend against, stated plainly: a Scanner implementation
// that builds its store query wrongly. That is one reviewable function per
// store rather than a property of every query, which is the trade this design
// is making.
type Engine struct {
	catalog  *Catalog
	policy   Policy
	budget   Budget
	scanners map[string]Scanner
}

// NewEngine wires scanners to the relations they serve.
//
// It fails if any catalog relation has no scanner. A relation that resolves but
// cannot be read would surface as a confusing runtime error on a query that
// passed validation; better to refuse to start.
func NewEngine(cat *Catalog, p Policy, b Budget, scanners ...Scanner) (*Engine, error) {
	byName := make(map[string]Scanner, len(scanners))
	for _, s := range scanners {
		byName[s.Relation().Name] = s
	}
	var missing []string
	for _, name := range cat.Names() {
		if _, ok := byName[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("sqlq: catalog relations have no scanner: %s", strings.Join(missing, ", "))
	}
	return &Engine{catalog: cat, policy: p, budget: b, scanners: byName}, nil
}

// Result is a query's output.
type Result struct {
	Columns []string
	Rows    [][]any
	Elapsed time.Duration
	Scanned int // rows pulled from stores, for cost attribution
	// Relations the validator resolved, for audit and cost attribution.
	// Carried on the result rather than recomputed by the caller: re-parsing the
	// statement later would answer with whatever the parser says *then*, which
	// is not necessarily what was acted on now.
	Relations []string
}

// Query validates, scans, loads and executes. tenantID must come from the
// authenticated request — never from anything the user sent as data.
// duckDSN opens the local execution engine with filesystem and network access
// switched off.
//
// # Why this is not redundant with the function denylist
//
// `deniedFunctions` names ClickHouse and MySQL functions, because that is the
// grammar the validator parses. The engine that actually *runs* the statement
// is DuckDB, whose file readers live in a different namespace — `read_csv_auto`,
// `read_text`, `read_blob`, `read_parquet`, `glob`. None of them were denied,
// and all of them passed validation.
//
// They were not reachable, for two reasons that are both accidents of the
// current front end: every one is a *table* function, and the MySQL grammar
// cannot express `FROM read_text('/etc/passwd')`. The policy's own comment
// warns against exactly this — "a denial that only works because of the current
// parser is a denial that disappears quietly when the parser changes" — and the
// DuckDB namespace is where that warning had not been applied.
//
// So the guarantee moves to the engine, where it does not depend on a parser or
// on a list being complete: with external access off, a file reader that somehow
// became reachable still cannot open anything. DuckDB defaults this to true.
const duckDSN = "?enable_external_access=false"

func (e *Engine) Query(ctx context.Context, tenantID, userSQL string) (*Result, error) {
	if strings.TrimSpace(tenantID) == "" {
		// Fail closed. An empty tenant that reached a scanner would be a request
		// to read everything.
		return nil, fmt.Errorf("sqlq: refusing to execute with an empty tenant")
	}

	analysis, err := Validate(userSQL, e.catalog, e.policy)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, e.budget.MaxWallClock)
	defer cancel()

	start := time.Now()

	// If the store can answer this shape itself, let it. Local execution has to
	// fetch every row an aggregate covers, which for the log relation is not
	// merely slow but impossible — Quickwit caps a search at 10,000 hits, so
	// `count(*)` over the corpus fails outright without this path.
	//
	// Push-down sends no user SQL anywhere: a recognised shape becomes a typed
	// method call on the scanner, which builds its own statement with the tenant
	// bound exactly as a row scan does.
	if plan := planPushdown(analysis); plan != nil {
		if agg, ok := e.scanners[plan.rel.Name].(Aggregator); ok {
			return e.runPushdown(ctx, tenantID, plan, agg, start)
		}
	}

	// A fresh in-memory database per query. Reusing one across queries would
	// mean one tenant's materialised rows outliving their request and being
	// visible to the next — the exact leak this design removes everywhere else.
	db, err := sql.Open("duckdb", duckDSN)
	if err != nil {
		return nil, fmt.Errorf("sqlq: open engine: %w", err)
	}
	defer db.Close()

	track := &tracker{budget: e.budget}
	for _, rel := range analysis.Relations {
		scanner := e.scanners[rel.Name]
		if scanner == nil {
			// Unreachable via NewEngine's check, but a nil dereference here would
			// be a crash on user input.
			return nil, fmt.Errorf("sqlq: no scanner for relation %q", rel.Name)
		}
		rows, err := scanner.Scan(ctx, tenantID, e.budget.MaxRowsPerRelation+1)
		if err != nil {
			return nil, fmt.Errorf("sqlq: scan %s: %w", rel.Name, err)
		}
		if err := track.add(rel.Name, len(rows.Values)); err != nil {
			return nil, err
		}
		if err := loadRelation(ctx, db, rel, rows); err != nil {
			return nil, fmt.Errorf("sqlq: load %s: %w", rel.Name, err)
		}
	}

	rendered, err := render(analysis)
	if err != nil {
		return nil, err
	}

	res, err := runQuery(ctx, db, rendered)
	if err != nil {
		return nil, err
	}
	res.Elapsed = time.Since(start)
	res.Scanned = track.total
	for _, rel := range analysis.Relations {
		res.Relations = append(res.Relations, rel.Name)
	}
	return res, nil
}

// runPushdown answers an aggregate from the store.
//
// Column names come from the plan, which took them from the user's statement,
// so the result matches the query they wrote rather than exposing the internal
// bucket names the store returned.
func (e *Engine) runPushdown(ctx context.Context, tenantID string, plan *pushdownPlan, agg Aggregator, start time.Time) (*Result, error) {
	res := &Result{Relations: []string{plan.rel.Name}}

	switch plan.kind {
	case pushCountAll:
		n, err := agg.CountAll(ctx, tenantID, plan.where)
		if err != nil {
			return nil, fmt.Errorf("sqlq: count %s: %w", plan.rel.Name, err)
		}
		res.Columns = []string{plan.countAlias}
		res.Rows = [][]any{{n}}

	case pushGroupCount:
		rows, err := agg.GroupCount(ctx, tenantID, plan.column, plan.where, plan.limit)
		if err != nil {
			return nil, fmt.Errorf("sqlq: group %s by %s: %w", plan.rel.Name, plan.column, err)
		}
		res.Columns = []string{plan.groupAlias, plan.countAlias}
		res.Rows = rows.Values

	default:
		return nil, fmt.Errorf("sqlq: unrecognised push-down plan")
	}

	res.Elapsed = time.Since(start)
	// Scanned stays zero deliberately. It counts rows pulled *into* the engine
	// for cost attribution, and push-down pulls none — reporting the store's
	// internal row count here would make an audit trail claim we moved data we
	// did not.
	return res, nil
}

// render turns the validated AST back into SQL for the local engine.
//
// Rendering from the AST rather than forwarding the user's text is deliberate.
// If the original string were passed through, the statement that executes would
// be parsed a second time, by a different parser, and could mean something
// other than what was validated. Re-rendering means what runs is derived from
// the tree that was checked.
//
// Identifiers are emitted double-quoted and strings single-quoted — ANSI, and
// unambiguous to DuckDB — rather than in the MySQL backquote style the parser
// reads.
func render(a *Analysis) (string, error) {
	var sb strings.Builder
	// RestoreStringWithoutCharset matters more than it looks. Without it the
	// parser re-emits string literals with a MySQL charset introducer —
	// `_UTF8MB4'initech'` — which DuckDB rejects outright. Caught by the
	// isolation tests rather than by review, and it is the concrete form of the
	// general risk in re-rendering across dialects: the tree is faithful, the
	// text need not be. Every accepted shape has to be exercised against the
	// engine that will run it.
	flags := format.RestoreStringSingleQuotes |
		format.RestoreStringWithoutCharset |
		format.RestoreKeyWordUppercase |
		format.RestoreNameDoubleQuotes |
		format.RestoreSpacesAroundBinaryOperation
	if err := a.Stmt.Restore(format.NewRestoreCtx(flags, &sb)); err != nil {
		// A statement that validated but cannot be re-rendered must not fall back
		// to the raw text — that would reintroduce exactly the double-parse this
		// function exists to prevent.
		return "", fmt.Errorf("sqlq: cannot render validated statement: %w", err)
	}
	return sb.String(), nil
}

// loadRelation creates the relation's table and inserts its rows.
func loadRelation(ctx context.Context, db *sql.DB, rel Relation, rows *Rows) error {
	cols := rows.Columns
	if len(cols) == 0 {
		cols = rel.Columns
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}

	// Relation names come from the catalog, never from user input, so they are
	// safe to interpolate — but they are still quoted so a future catalog entry
	// cannot change the statement's shape.
	create := fmt.Sprintf(`CREATE TABLE %q (%s)`,
		rel.Name, strings.Join(typedColumns(quoted, cols, rows.Values), ", "))
	if _, err := db.ExecContext(ctx, create); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if len(rows.Values) == 0 {
		return nil
	}

	placeholders := "(" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	insert := fmt.Sprintf(`INSERT INTO %q (%s) VALUES %s`,
		rel.Name, strings.Join(quoted, ", "), placeholders)
	stmt, err := db.PrepareContext(ctx, insert)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	bind := make([]any, len(cols))
	for _, v := range rows.Values {
		if len(v) != len(cols) {
			return fmt.Errorf("row has %d values for %d columns", len(v), len(cols))
		}
		for i, cell := range v {
			bind[i] = normalise(cell)
		}
		if _, err := stmt.ExecContext(ctx, bind...); err != nil {
			return fmt.Errorf("insert: %w", err)
		}
	}
	return nil
}

// normalise converts a scanned value into something the DuckDB driver can bind.
//
// Scanners return whatever their store's driver produced: database/sql yields
// []byte for text columns and time.Time for timestamps, and ClickHouse's JSON
// decodes into float64 and nil. Passing those through unchanged fails with
// "could not bind parameter" — found by running against real Postgres, not by
// any unit test, because the in-memory fixtures were all strings.
func normalise(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case time.Time:
		return x
	default:
		return v
	}
}

// typedColumns gives each column a DuckDB type inferred from the data.
//
// Declaring everything VARCHAR would load without error and then quietly break
// arithmetic: avg(duration_ms) over strings is either a cast failure or, worse,
// a lexicographic comparison that returns a plausible wrong number. Types are
// inferred from the first non-nil value in each column, which is what the
// store's own driver already decided; a column that is entirely NULL falls back
// to VARCHAR because there is nothing to infer from.
func typedColumns(quoted, names []string, values [][]any) []string {
	out := make([]string, len(quoted))
	for i := range quoted {
		out[i] = quoted[i] + " " + duckType(columnSample(values, i))
	}
	return out
}

// columnSample returns the first non-nil value in a column, or nil.
func columnSample(values [][]any, col int) any {
	for _, row := range values {
		if col < len(row) && row[col] != nil {
			return row[col]
		}
	}
	return nil
}

func duckType(sample any) string {
	switch sample.(type) {
	case nil:
		return "VARCHAR"
	case bool:
		return "BOOLEAN"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "BIGINT"
	case float32, float64:
		// ClickHouse's JSONCompact decodes every number as float64, so integer
		// columns arrive here as floats. DOUBLE is the honest type for what was
		// actually received; claiming BIGINT would truncate.
		return "DOUBLE"
	case time.Time:
		return "TIMESTAMP"
	default:
		return "VARCHAR"
	}
}

func runQuery(ctx context.Context, db *sql.DB, query string) (*Result, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("sqlq: execute: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("sqlq: columns: %w", err)
	}

	out := &Result{Columns: cols}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("sqlq: scan result: %w", err)
		}
		out.Rows = append(out.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlq: result iteration: %w", err)
	}
	return out, nil
}

// Schema describes the relations this engine will accept in a statement.
func (e *Engine) Schema() []SchemaRelation { return e.catalog.Schema() }
