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
