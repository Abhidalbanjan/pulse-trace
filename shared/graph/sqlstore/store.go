// Package sqlstore is the SQL implementation of the topology port (P1.4).
//
// # Why SQL can hold a graph here
//
// "Graph database" is not what the topology needed; it is what it was built on.
// The Cypher this replaces is ten statements, every one of them depth-1 or a
// whole-tenant scan — no variable-length paths, no shortest-path, no traversal
// of any kind. That is an adjacency list with extra operational cost, and an
// adjacency list is two tables.
//
// If a genuine traversal is ever needed, this is where a recursive CTE goes,
// and it gets its own slice with its own gate rather than being written
// speculatively now.
//
// # Tenant isolation is a constraint, not a convention
//
// tenant_id leads the primary key of both tables and every statement filters on
// it. The Neo4j original achieved the same by keying each MERGE on
// {name, tenant}, which works exactly as long as every query remembers to. Here
// the database enforces it: two tenants running a service of the same name
// cannot collide, because the key says so.
package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	sharddb "github.com/pulsetrace/shared/db"
	"github.com/pulsetrace/shared/graph"
)

// Store implements graph.GraphStore over any SQL backend the dialect layer
// supports.
type Store struct {
	db      *sql.DB
	dialect sharddb.Dialect
}

// New wires a store to an open database.
func New(db *sql.DB, d sharddb.Dialect) *Store { return &Store{db: db, dialect: d} }

var _ graph.GraphStore = (*Store)(nil)

// q rebinds a Postgres-style statement for the active dialect. Statements are
// written once, in $n form, and translated at the seam.
func (s *Store) q(query string) string { return sharddb.Rebind(s.dialect, query) }

// requireTenant fails closed on an empty tenant.
//
// Every method scopes on tenant_id, so an empty one would silently address the
// rows of a tenant literally named "" — and, worse, DeleteTenant would delete
// them. The Neo4j original has the same hazard and no guard; adding one here is
// not a behaviour change anybody can observe except by making the mistake.
func requireTenant(tenant string) error {
	if strings.TrimSpace(tenant) == "" {
		return fmt.Errorf("graph/sqlstore: refusing to operate with an empty tenant")
	}
	return nil
}

// ── Writes ───────────────────────────────────────────────────────────────────

// ensureNode creates a service if it is not already there, touching no fields.
//
// This is `MERGE (s:Service {name, tenant})` with no SET: match or create, and
// never overwrite. Using an upsert that wrote defaults would let an edge write
// blank out a service's catalog metadata, which is the kind of loss that shows
// up as "the team column emptied itself overnight".
func (s *Store) ensureNode(ctx context.Context, tx *sql.Tx, tenant, name string) error {
	_, err := tx.ExecContext(ctx, s.q(`
		INSERT INTO graph_nodes (tenant_id, name) VALUES ($1, $2)
		ON CONFLICT (tenant_id, name) DO NOTHING`), tenant, name)
	return err
}

func (s *Store) UpsertDependencyEdge(ctx context.Context, tenant, from, to string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Both endpoints first: an edge write is also how a node comes into
	// existence, matching the three MERGEs it replaces.
	if err := s.ensureNode(ctx, tx, tenant, from); err != nil {
		return err
	}
	if err := s.ensureNode(ctx, tx, tenant, to); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`
		INSERT INTO graph_edges (tenant_id, source, target) VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, source, target) DO NOTHING`), tenant, from, to); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateServiceState(ctx context.Context, tenant, service, state string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO graph_nodes (tenant_id, name, state) VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, name) DO UPDATE SET state = excluded.state`),
		tenant, service, state)
	return err
}

func (s *Store) UpsertServiceCatalog(ctx context.Context, tenant, service, team, repo, slack string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO graph_nodes (tenant_id, name, team, repo, slack) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, name) DO UPDATE
		SET team = excluded.team, repo = excluded.repo, slack = excluded.slack`),
		tenant, service, team, repo, slack)
	return err
}

func (s *Store) UpsertServiceMetadata(ctx context.Context, tenant, service, tier, lifecycle string, links map[string]string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO graph_nodes (tenant_id, name, tier, lifecycle, links) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, name) DO UPDATE
		SET tier = excluded.tier, lifecycle = excluded.lifecycle, links = excluded.links`),
		tenant, service, tier, lifecycle, encodeLinks(links))
	return err
}

// encodeLinks matches the Neo4j property exactly, "{}" and all: node properties
// there cannot hold nested maps, so links have always been a JSON string, and
// keeping the same encoding means a database migrated between the two reads
// back identically.
func encodeLinks(links map[string]string) string {
	if len(links) == 0 {
		return "{}"
	}
	b, err := json.Marshal(links)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeLinks(raw string) map[string]string {
	if raw == "" || raw == "{}" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}

// ── Reads ────────────────────────────────────────────────────────────────────

func (s *Store) GetDownstreamDependencies(ctx context.Context, tenant, service string) ([]string, error) {
	// Downstream = the services that depend on this one, i.e. edges pointing at
	// it. The Cypher reads `(upstream {name})<-[:DEPENDS_ON]-(downstream)`,
	// which is the arrow the name does not obviously imply.
	return s.names(ctx, `SELECT source FROM graph_edges WHERE tenant_id = $1 AND target = $2 ORDER BY source`, tenant, service)
}

func (s *Store) GetUpstreamDependencies(ctx context.Context, tenant, service string) ([]string, error) {
	return s.names(ctx, `SELECT target FROM graph_edges WHERE tenant_id = $1 AND source = $2 ORDER BY target`, tenant, service)
}

func (s *Store) names(ctx context.Context, query, tenant, service string) ([]string, error) {
	if err := requireTenant(tenant); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.q(query), tenant, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetServiceState(ctx context.Context, tenant, service string) (string, error) {
	if err := requireTenant(tenant); err != nil {
		return "", err
	}
	var state string
	err := s.db.QueryRowContext(ctx,
		s.q(`SELECT state FROM graph_nodes WHERE tenant_id = $1 AND name = $2`),
		tenant, service).Scan(&state)
	if err == sql.ErrNoRows {
		// An unknown service is not an error: callers ask about services that
		// may not have been observed yet.
		return "", nil
	}
	return state, err
}

func (s *Store) GetGraph(ctx context.Context, tenant string) (*graph.Graph, error) {
	if err := requireTenant(tenant); err != nil {
		return nil, err
	}
	out := &graph.Graph{}

	// Ordered, unlike the Cypher, which returns whatever order the store feels
	// like. Deterministic output costs nothing here and makes the response
	// diffable between runs and between implementations.
	nodeRows, err := s.db.QueryContext(ctx, s.q(`
		SELECT name, state, team, repo, slack, tier, lifecycle, links
		FROM graph_nodes WHERE tenant_id = $1 ORDER BY name`), tenant)
	if err != nil {
		return nil, err
	}
	defer nodeRows.Close()
	for nodeRows.Next() {
		var n graph.Node
		var links string
		if err := nodeRows.Scan(&n.Id, &n.State, &n.Team, &n.Repo, &n.Slack, &n.Tier, &n.Lifecycle, &links); err != nil {
			return nil, err
		}
		if n.State == "" {
			// No signal is the healthy case, and the UI has always rendered it
			// that way. Storing "" and presenting HEALTHY keeps "never
			// evaluated" and "evaluated healthy" distinguishable in the table.
			n.State = "HEALTHY"
		}
		n.Links = decodeLinks(links)
		out.Nodes = append(out.Nodes, n)
	}
	if err := nodeRows.Err(); err != nil {
		return nil, err
	}

	edgeRows, err := s.db.QueryContext(ctx, s.q(`
		SELECT source, target, causal_entries
		FROM graph_edges WHERE tenant_id = $1 ORDER BY source, target`), tenant)
	if err != nil {
		return nil, err
	}
	defer edgeRows.Close()
	for edgeRows.Next() {
		var e graph.Edge
		var entries string
		if err := edgeRows.Scan(&e.Source, &e.Target, &entries); err != nil {
			return nil, err
		}
		reasons := reasonsOf(decodeEntries(entries))
		e.IsCausal = len(reasons) > 0
		e.Reason = strings.Join(reasons, "; ")
		out.Edges = append(out.Edges, e)
	}
	return out, edgeRows.Err()
}

func (s *Store) DeleteTenant(ctx context.Context, tenant string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM graph_edges WHERE tenant_id = $1`), tenant); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM graph_nodes WHERE tenant_id = $1`), tenant); err != nil {
		return err
	}
	return tx.Commit()
}

// ── Causal path ──────────────────────────────────────────────────────────────

// causal_entries is a JSON array of "incidentID::reason" strings.
//
// A list rather than a single is_causal/reason pair because two incidents
// analysed at the same time both legitimately implicate the same edge, and a
// scalar makes the second overwrite the first. The encoding is the Neo4j
// property's, unchanged, so the two implementations read each other's data.

func decodeEntries(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func encodeEntries(entries []string) string {
	if len(entries) == 0 {
		return "[]"
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func reasonsOf(entries []string) []string {
	var reasons []string
	for _, e := range entries {
		if _, r, found := strings.Cut(e, "::"); found {
			reasons = append(reasons, r)
		}
	}
	return reasons
}

func (s *Store) UpdateCausalPath(ctx context.Context, tenant, incidentID string, links []graph.CausalLink) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	if incidentID == "" {
		return fmt.Errorf("graph/sqlstore: incidentID is required")
	}

	// One transaction for clear-then-set. The Cypher runs the two as separate
	// statements and can be observed halfway through, showing an incident with
	// no highlighted path at all; there is no reason to reproduce that.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Drop this incident's own prior contributions, so a re-analysis that
	// found a shorter chain does not leave the old one highlighted.
	if err := s.clearIncident(ctx, tx, tenant, incidentID); err != nil {
		return err
	}

	// 2. Add the new ones. Direction is not significant: an incident
	// implicating A→B highlights the edge whichever way it was recorded.
	for _, link := range links {
		if err := s.addEntry(ctx, tx, tenant, incidentID, link); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) clearIncident(ctx context.Context, tx *sql.Tx, tenant, incidentID string) error {
	type row struct{ source, target, entries string }
	var rowsToFix []row

	// Read-modify-write rather than a JSON operator: the two backends spell
	// JSON array manipulation differently and this list is a handful of entries
	// per edge. Correctness in one place beats two dialect-specific mutations.
	q, err := tx.QueryContext(ctx, s.q(`
		SELECT source, target, causal_entries FROM graph_edges
		WHERE tenant_id = $1 AND causal_entries <> '[]' AND causal_entries <> ''`), tenant)
	if err != nil {
		return err
	}
	for q.Next() {
		var r row
		if err := q.Scan(&r.source, &r.target, &r.entries); err != nil {
			q.Close()
			return err
		}
		rowsToFix = append(rowsToFix, r)
	}
	q.Close()
	if err := q.Err(); err != nil {
		return err
	}

	prefix := incidentID + "::"
	for _, r := range rowsToFix {
		before := decodeEntries(r.entries)
		kept := make([]string, 0, len(before))
		for _, e := range before {
			if !strings.HasPrefix(e, prefix) {
				kept = append(kept, e)
			}
		}
		if len(kept) == len(before) {
			continue // nothing of this incident's here
		}
		if _, err := tx.ExecContext(ctx, s.q(`
			UPDATE graph_edges SET causal_entries = $1
			WHERE tenant_id = $2 AND source = $3 AND target = $4`),
			encodeEntries(kept), tenant, r.source, r.target); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) addEntry(ctx context.Context, tx *sql.Tx, tenant, incidentID string, link graph.CausalLink) error {
	entry := incidentID + "::" + link.Reason

	// Each endpoint is bound twice rather than reusing $2/$3.
	//
	// This is written to be correct on either side of a fix in flight. On main
	// today, Rebind emits a bare `?`, which is positional *by occurrence*: `$3`
	// appearing twice becomes two placeholders for one argument, and the arity
	// stops matching. The conformance suite found that on its first run, which
	// is how the dialect layer came to be changed to SQLite's numbered `?n`
	// form, where reuse survives exactly.
	//
	// Binding both endpoints explicitly needs neither behaviour, so this does
	// not have to be revisited when the two branches meet.
	q, err := tx.QueryContext(ctx, s.q(`
		SELECT source, target, causal_entries FROM graph_edges
		WHERE tenant_id = $1 AND ((source = $2 AND target = $3) OR (source = $4 AND target = $5))`),
		tenant, link.Source, link.Target, link.Target, link.Source)
	if err != nil {
		return err
	}
	type row struct{ source, target, entries string }
	var matched []row
	for q.Next() {
		var r row
		if err := q.Scan(&r.source, &r.target, &r.entries); err != nil {
			q.Close()
			return err
		}
		matched = append(matched, r)
	}
	q.Close()
	if err := q.Err(); err != nil {
		return err
	}

	for _, r := range matched {
		next := append(decodeEntries(r.entries), entry)
		if _, err := tx.ExecContext(ctx, s.q(`
			UPDATE graph_edges SET causal_entries = $1
			WHERE tenant_id = $2 AND source = $3 AND target = $4`),
			encodeEntries(next), tenant, r.source, r.target); err != nil {
			return err
		}
	}
	return nil
}
