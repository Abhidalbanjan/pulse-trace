package sqlq

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Postgres-backed relations (the control plane).

// pgTable is the physical binding for one catalog relation.
type pgTable struct {
	table   string
	columns []chColumn // reused: {logical, expr} has the same shape here
}

var pgTables = map[string]pgTable{
	"deployments": {
		table: "deployments",
		columns: []chColumn{
			{"id", "id"},
			{"service", "service"},
			{"version", "version"},
			{"git_sha", "git_sha"},
			{"environment", "environment"},
			{"deployed_by", "deployed_by"},
			{"deployed_at", "deployed_at"},
			// `notes` is deliberately not exposed: free text written by whoever
			// recorded the deploy, with no expectation of being queryable.
		},
	},
	"incidents": {
		table: "incidents",
		columns: []chColumn{
			{"id", "id"},
			{"title", "title"},
			{"status", "status"},
			{"severity", "severity"},
			{"root_cause", "root_cause"},
			{"alert_count", "alert_count"},
			{"started_at", "started_at"},
			{"resolved_at", "resolved_at"},
			// `causal` (jsonb) is omitted: it is an internal analysis artifact
			// whose shape is not a contract with anyone.
		},
	},
}

// PostgresScanner materialises one Postgres-backed relation.
type PostgresScanner struct {
	Rel Relation
	DB  *sql.DB
}

func (s *PostgresScanner) Relation() Relation { return s.Rel }

// statement returns the parameterised SQL and is split out so the tenant
// predicate can be asserted without a database.
//
// $1 is the tenant and $2 the limit. Both are bind parameters: there is no
// string interpolation of any runtime value anywhere in this file, and the only
// interpolated things — table and column names — come from the map above, which
// is compile-time data.
func (s *PostgresScanner) statement() (string, error) {
	t, ok := pgTables[s.Rel.Name]
	if !ok {
		return "", fmt.Errorf("postgres scanner: no physical mapping for relation %q", s.Rel.Name)
	}
	projections := make([]string, len(t.columns))
	for i, c := range t.columns {
		projections[i] = fmt.Sprintf("%s AS %s", c.expr, c.logical)
	}
	return fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = $1 LIMIT $2",
		strings.Join(projections, ", "), t.table), nil
}

func (s *PostgresScanner) Scan(ctx context.Context, tenantID string, limit int) (*Rows, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("postgres scanner %s: refusing to scan with an empty tenant", s.Rel.Name)
	}
	stmt, err := s.statement()
	if err != nil {
		return nil, err
	}

	rows, err := s.DB.QueryContext(ctx, stmt, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres scanner %s: %w", s.Rel.Name, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := &Rows{Columns: cols}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("postgres scanner %s: scan: %w", s.Rel.Name, err)
		}
		out.Values = append(out.Values, vals)
	}
	return out, rows.Err()
}

// filterClause renders the pushed-down predicates as SQL and returns the values
// to bind, starting at $next. Column expressions come from the compile-time
// mapping; every value is a placeholder, so no user text enters the statement.
func (t pgTable) filterClause(where []Predicate, next int) (string, []any, error) {
	if len(where) == 0 {
		return "", nil, nil
	}
	args := make([]any, 0, len(where))
	var clause strings.Builder
	for _, p := range where {
		expr := ""
		for _, c := range t.columns {
			if strings.EqualFold(c.logical, p.Column) {
				expr = c.expr
				break
			}
		}
		if expr == "" {
			return "", nil, fmt.Errorf("postgres scanner: %q is not a mapped column of %s", p.Column, t.table)
		}
		clause.WriteString(fmt.Sprintf(" AND %s = $%d", expr, next))
		args = append(args, p.Value)
		next++
	}
	return clause.String(), args, nil
}

// ── Aggregator ───────────────────────────────────────────────────────────────

func (s *PostgresScanner) CountAll(ctx context.Context, tenantID string, where []Predicate) (int64, error) {
	t, ok := pgTables[s.Rel.Name]
	if !ok {
		return 0, fmt.Errorf("postgres scanner: no physical mapping for relation %q", s.Rel.Name)
	}
	if strings.TrimSpace(tenantID) == "" {
		return 0, fmt.Errorf("postgres scanner %s: refusing to count with an empty tenant", s.Rel.Name)
	}
	filter, args, err := t.filterClause(where, 2)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.DB.QueryRowContext(ctx,
		fmt.Sprintf("SELECT count(*) FROM %s WHERE tenant_id = $1%s", t.table, filter),
		append([]any{tenantID}, args...)...).Scan(&n)
	return n, err
}

func (s *PostgresScanner) GroupCount(ctx context.Context, tenantID, column string, where []Predicate, limit int) (*Rows, error) {
	t, ok := pgTables[s.Rel.Name]
	if !ok {
		return nil, fmt.Errorf("postgres scanner: no physical mapping for relation %q", s.Rel.Name)
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("postgres scanner %s: refusing to group with an empty tenant", s.Rel.Name)
	}
	expr := ""
	for _, c := range t.columns {
		if strings.EqualFold(c.logical, column) {
			expr = c.expr
			break
		}
	}
	if expr == "" {
		return nil, fmt.Errorf("postgres scanner: %q is not a mapped column of %s", column, s.Rel.Name)
	}
	// expr and table come from the compile-time mapping; tenant, filter values
	// and limit are bind parameters. Nothing here is user text.
	//
	// The limit is bound last so the filter placeholders can occupy $2..$n
	// without renumbering it by hand.
	filter, args, err := t.filterClause(where, 2)
	if err != nil {
		return nil, err
	}
	stmt := fmt.Sprintf(
		"SELECT %s AS %s, count(*) AS count FROM %s WHERE tenant_id = $1%s GROUP BY %s ORDER BY count DESC LIMIT $%d",
		expr, column, t.table, filter, expr, len(args)+2)
	rows, err := s.DB.QueryContext(ctx, stmt, append(append([]any{tenantID}, args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("postgres scanner %s: %w", s.Rel.Name, err)
	}
	defer rows.Close()

	out := &Rows{Columns: []string{column, "count"}}
	for rows.Next() {
		var key any
		var n int64
		if err := rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		out.Values = append(out.Values, []any{key, n})
	}
	return out, rows.Err()
}
