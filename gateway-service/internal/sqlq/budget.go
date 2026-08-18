package sqlq

import (
	"fmt"
	"time"
)

// Budget caps what one query may consume.
//
// Caps are enforced per relation *and* in total. Per-relation alone is not
// enough — a five-way join under a one-million-row per-relation cap is five
// million rows pulled into memory, and the whole point of executing locally is
// that the rows arrive here. Total alone is not enough either, because it does
// not stop one pathological relation from consuming the entire allowance before
// the others are read.
type Budget struct {
	MaxRowsPerRelation int
	MaxTotalRows       int
	MaxWallClock       time.Duration
}

// DefaultBudget is sized for an interactive query, not a batch export.
//
// A user who needs more rows than this wants an export, which is a different
// feature with different economics; letting an interactive endpoint quietly
// become one is how a query surface turns into an outage.
func DefaultBudget() Budget {
	return Budget{
		MaxRowsPerRelation: 500_000,
		MaxTotalRows:       1_000_000,
		MaxWallClock:       30 * time.Second,
	}
}

// BudgetError is returned when a query exceeds its allowance. It is separate
// from RejectionError because the query was legitimate — it asked for too much,
// which is a different conversation with the user than "you may not ask that".
type BudgetError struct {
	Limit  string
	Detail string
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("query budget exceeded (%s): %s", e.Limit, e.Detail)
}

// tracker accumulates usage across a single query's scans.
type tracker struct {
	budget Budget
	total  int
}

func (t *tracker) add(relation string, n int) error {
	if n > t.budget.MaxRowsPerRelation {
		return &BudgetError{
			Limit:  "rows_per_relation",
			Detail: fmt.Sprintf("relation %q returned %d rows, limit is %d", relation, n, t.budget.MaxRowsPerRelation),
		}
	}
	t.total += n
	if t.total > t.budget.MaxTotalRows {
		return &BudgetError{
			Limit:  "total_rows",
			Detail: fmt.Sprintf("query scanned %d rows across all relations, limit is %d", t.total, t.budget.MaxTotalRows),
		}
	}
	return nil
}
