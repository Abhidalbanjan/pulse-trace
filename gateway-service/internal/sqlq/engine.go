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
}

// Query validates, scans, loads and executes. tenantID must come from the
// authenticated request — never from anything the user sent as data.
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

	// A fresh in-memory database per query. Reusing one across queries would
	// mean one tenant's materialised rows outliving their request and being
	// visible to the next — the exact leak this design removes everywhere else.
	db, err := sql.Open("duckdb", "")
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

	// Columns are typed VARCHAR only where we have no better information; DuckDB
	// infers from the inserted values otherwise. Relation names come from the
	// catalog, never from user input, so they are safe to interpolate — but they
	// are still quoted so a future catalog entry cannot change the statement's
	// shape.
	create := fmt.Sprintf(`CREATE TABLE %q (%s)`,
		rel.Name, strings.Join(typedColumns(quoted), ", "))
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

	for _, v := range rows.Values {
		if len(v) != len(cols) {
			return fmt.Errorf("row has %d values for %d columns", len(v), len(cols))
		}
		if _, err := stmt.ExecContext(ctx, v...); err != nil {
			return fmt.Errorf("insert: %w", err)
		}
	}
	return nil
}

func typedColumns(quoted []string) []string {
	out := make([]string, len(quoted))
	for i, q := range quoted {
		out[i] = q + " VARCHAR"
	}
	return out
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
