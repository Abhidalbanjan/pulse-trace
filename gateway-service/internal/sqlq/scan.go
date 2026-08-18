package sqlq

import (
	"context"
	"fmt"
)

// Rows is one relation's materialised contents for one tenant.
type Rows struct {
	Columns []string
	Values  [][]any
}

// Scanner materialises exactly one relation, for exactly one tenant.
//
// This interface carries the isolation guarantee, and its shape is the
// guarantee. Note what a Scanner is *not* given: it never receives the user's
// SQL, or any fragment of it, or any predicate derived from it. It is handed a
// tenant ID that came from the authenticated request and nothing else, so there
// is no channel through which a query could influence which tenant's rows are
// produced.
//
// That is why this is stronger than validating the user's SQL. Validation
// answers "is this statement acceptable?", and any such check is one parser
// disagreement away from being wrong. This answers "could the statement have
// changed which rows were fetched?" — and the answer is no, because the
// statement is not an input to fetching. The user's SQL runs afterwards, over
// data that is already single-tenant.
//
// A Scanner implementation must therefore bind the tenant into its own store
// query as a parameter. Building the store query by concatenating anything
// user-supplied would give the whole thing away, which is why implementations
// live behind this interface rather than being written per call site.
type Scanner interface {
	Relation() Relation
	Scan(ctx context.Context, tenantID string, limit int) (*Rows, error)
}

// StaticScanner serves a fixed set of rows per tenant. It exists so the engine
// and its isolation properties can be tested without standing up three stores,
// and so a relation can be served from memory where that is genuinely correct.
type StaticScanner struct {
	Rel      Relation
	ByTenant map[string]*Rows
}

func (s *StaticScanner) Relation() Relation { return s.Rel }

func (s *StaticScanner) Scan(_ context.Context, tenantID string, limit int) (*Rows, error) {
	if tenantID == "" {
		// Defence in depth: the engine refuses an empty tenant before reaching a
		// scanner, but a scanner that would serve every tenant when asked for
		// none is a landmine for the next caller.
		return nil, fmt.Errorf("scanner %s: refusing to scan with an empty tenant", s.Rel.Name)
	}
	rows, ok := s.ByTenant[tenantID]
	if !ok {
		return &Rows{Columns: s.Rel.Columns}, nil
	}
	if limit > 0 && len(rows.Values) > limit {
		return &Rows{Columns: rows.Columns, Values: rows.Values[:limit]}, nil
	}
	return rows, nil
}
