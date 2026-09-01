package graph_test

// The conformance suite.
//
// # Why this file is the deliverable
//
// Two implementations of one interface are two products unless something forces
// them to agree. The compiler checks the *shape* of GraphStore and nothing about
// its meaning: a store that dropped causal entries, or resolved an edge write
// without creating its endpoints, or let one tenant's service shadow another's,
// would satisfy the interface perfectly.
//
// So every assertion here runs against every implementation, and the assertions
// are the surprising parts of the Neo4j behaviour rather than the obvious ones.
// The obvious parts do not break.
//
// # The gap, named rather than hidden
//
// Only the SQL store runs today. The Neo4j adapter is the next commit, and the
// factory list below is where it slots in — deliberately structured so adding it
// is one entry and not a rewrite. Until then this proves two things worth
// having: that the SQL implementation satisfies the contract, and that the
// contract is executable at all. It does not yet prove the two agree, and the
// plan's "equivalence test" gate is not met until it does.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sharddb "github.com/pulsetrace/shared/db"
	_ "github.com/pulsetrace/shared/db/driver/sqlite"
	"github.com/pulsetrace/shared/graph"
	"github.com/pulsetrace/shared/graph/sqlstore"
)

// storeFactory builds a store and returns it with a cleanup.
type storeFactory struct {
	name string
	open func(t *testing.T) graph.GraphStore
}

func conformanceStores(t *testing.T) []storeFactory {
	t.Helper()
	return []storeFactory{
		{name: "sql/sqlite", open: openSQLiteStore},
		// {name: "neo4j", open: openNeo4jStore} — next commit; skipped without
		// NEO4J_CONFORMANCE=1, exactly as the bus suite skips Kafka.
	}
}

// openSQLiteStore gives each test its own database with the real schema applied
// — the migration file, not a hand-written CREATE TABLE. A test schema that
// drifts from the shipped one tests nothing about what runs.
func openSQLiteStore(t *testing.T) graph.GraphStore {
	t.Helper()
	conn, d, err := sharddb.Open(context.Background(), filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	raw, err := os.ReadFile(filepath.Join("..", "..", "topology-service", "migrations", "001_create_graph_topology.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, stmt := range sharddb.ExpandStatements(string(raw)) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if err := sharddb.ExecStatement(context.Background(), conn, d, stmt); err != nil {
			t.Fatalf("apply migration: %v\n  %.120s", err, strings.ReplaceAll(stmt, "\n", " "))
		}
	}
	return sqlstore.New(conn, d)
}

// forEachStore runs one assertion against every implementation.
func forEachStore(t *testing.T, name string, fn func(t *testing.T, s graph.GraphStore)) {
	t.Helper()
	for _, f := range conformanceStores(t) {
		t.Run(f.name+"/"+name, func(t *testing.T) { fn(t, f.open(t)) })
	}
}

const tenant = "acme"

func ctx() context.Context { return context.Background() }

// ── The contract ─────────────────────────────────────────────────────────────

// An edge write creates both endpoints.
//
// The Cypher is three MERGEs, so recording a dependency is also how a service
// comes into existence. A store that only wrote the edge would leave a graph
// whose nodes list is missing every service that is purely a dependency target.
func TestConformanceEdgeWriteCreatesBothServices(t *testing.T) {
	forEachStore(t, "edge creates nodes", func(t *testing.T, s graph.GraphStore) {
		if err := s.UpsertDependencyEdge(ctx(), tenant, "api", "billing"); err != nil {
			t.Fatalf("upsert edge: %v", err)
		}
		g, err := s.GetGraph(ctx(), tenant)
		if err != nil {
			t.Fatalf("get graph: %v", err)
		}
		if len(g.Nodes) != 2 {
			t.Fatalf("got %d nodes, want 2: %+v", len(g.Nodes), g.Nodes)
		}
		if len(g.Edges) != 1 || g.Edges[0].Source != "api" || g.Edges[0].Target != "billing" {
			t.Fatalf("edge wrong: %+v", g.Edges)
		}
	})
}

// Writing an edge must not blank a service's catalog fields.
//
// This is the failure an upsert-with-defaults produces, and it is silent: the
// team column empties itself the next time traffic is observed.
func TestConformanceEdgeWriteDoesNotClobberCatalog(t *testing.T) {
	forEachStore(t, "edge preserves catalog", func(t *testing.T, s graph.GraphStore) {
		mustCatalog(t, s, "api", "platform", "org/api", "#api")
		if err := s.UpsertDependencyEdge(ctx(), tenant, "api", "billing"); err != nil {
			t.Fatalf("upsert edge: %v", err)
		}
		n := nodeNamed(t, s, "api")
		if n.Team != "platform" || n.Repo != "org/api" || n.Slack != "#api" {
			t.Errorf("catalog lost after an edge write: %+v", n)
		}
	})
}

// The two edit surfaces are disjoint and must not overwrite each other.
func TestConformanceCatalogAndMetadataDoNotClobber(t *testing.T) {
	forEachStore(t, "disjoint edit surfaces", func(t *testing.T, s graph.GraphStore) {
		mustCatalog(t, s, "api", "platform", "org/api", "#api")
		if err := s.UpsertServiceMetadata(ctx(), tenant, "api", "tier-1", "production",
			map[string]string{"runbook": "https://rb"}); err != nil {
			t.Fatalf("upsert metadata: %v", err)
		}
		if err := s.UpdateServiceState(ctx(), tenant, "api", "DEGRADED"); err != nil {
			t.Fatalf("update state: %v", err)
		}

		n := nodeNamed(t, s, "api")
		if n.Team != "platform" || n.Tier != "tier-1" || n.Lifecycle != "production" || n.State != "DEGRADED" {
			t.Errorf("a later write clobbered an earlier surface: %+v", n)
		}
		if n.Links["runbook"] != "https://rb" {
			t.Errorf("links did not round-trip: %+v", n.Links)
		}
	})
}

// Upstream and downstream are opposite directions, and the naming is the part
// people get wrong: downstream is who depends on *you*.
func TestConformanceDependencyDirections(t *testing.T) {
	forEachStore(t, "directions", func(t *testing.T, s graph.GraphStore) {
		// api -> billing -> ledger
		mustEdge(t, s, "api", "billing")
		mustEdge(t, s, "billing", "ledger")

		up, err := s.GetUpstreamDependencies(ctx(), tenant, "billing")
		if err != nil {
			t.Fatalf("upstream: %v", err)
		}
		if len(up) != 1 || up[0] != "ledger" {
			t.Errorf("upstream of billing = %v, want [ledger]", up)
		}

		down, err := s.GetDownstreamDependencies(ctx(), tenant, "billing")
		if err != nil {
			t.Fatalf("downstream: %v", err)
		}
		if len(down) != 1 || down[0] != "api" {
			t.Errorf("downstream of billing = %v, want [api]", down)
		}
	})
}

// A service nobody has heard of is not an error.
func TestConformanceUnknownServiceStateIsEmptyNotAnError(t *testing.T) {
	forEachStore(t, "unknown service", func(t *testing.T, s graph.GraphStore) {
		state, err := s.GetServiceState(ctx(), tenant, "never-seen")
		if err != nil {
			t.Fatalf("unknown service must not error: %v", err)
		}
		if state != "" {
			t.Errorf("state = %q, want empty", state)
		}
	})
}

// No state reads back as HEALTHY.
func TestConformanceStatelessServiceReadsAsHealthy(t *testing.T) {
	forEachStore(t, "healthy default", func(t *testing.T, s graph.GraphStore) {
		mustEdge(t, s, "api", "billing")
		if got := nodeNamed(t, s, "api").State; got != "HEALTHY" {
			t.Errorf("state = %q, want HEALTHY", got)
		}
	})
}

// Two incidents may implicate the same edge, and neither may erase the other.
//
// This is why causal data is a list of "incidentID::reason" and not a scalar:
// two analyses running at once both legitimately touch the same dependency.
func TestConformanceConcurrentIncidentsShareAnEdge(t *testing.T) {
	forEachStore(t, "two incidents one edge", func(t *testing.T, s graph.GraphStore) {
		mustEdge(t, s, "api", "billing")
		mustCausal(t, s, "INC-1", "api", "billing", "latency spike")
		mustCausal(t, s, "INC-2", "api", "billing", "error budget burn")

		e := edgeBetween(t, s, "api", "billing")
		if !e.IsCausal {
			t.Fatal("edge is not marked causal")
		}
		if !strings.Contains(e.Reason, "latency spike") || !strings.Contains(e.Reason, "error budget burn") {
			t.Errorf("one incident erased the other: %q", e.Reason)
		}
	})
}

// Re-analysing an incident replaces its own entries and only its own.
func TestConformanceReanalysisClearsOnlyItsOwnEntries(t *testing.T) {
	forEachStore(t, "reanalysis", func(t *testing.T, s graph.GraphStore) {
		mustEdge(t, s, "api", "billing")
		mustCausal(t, s, "INC-1", "api", "billing", "first guess")
		mustCausal(t, s, "INC-2", "api", "billing", "other incident")

		// INC-1 re-analysed, and this time it implicates nothing.
		if err := s.UpdateCausalPath(ctx(), tenant, "INC-1", nil); err != nil {
			t.Fatalf("reanalyse: %v", err)
		}

		e := edgeBetween(t, s, "api", "billing")
		if strings.Contains(e.Reason, "first guess") {
			t.Errorf("a stale conclusion survived re-analysis: %q", e.Reason)
		}
		if !strings.Contains(e.Reason, "other incident") {
			t.Errorf("re-analysing one incident cleared another's: %q", e.Reason)
		}
	})
}

// Causal links match an edge whichever way round they are given.
func TestConformanceCausalLinkDirectionIsNotSignificant(t *testing.T) {
	forEachStore(t, "causal direction", func(t *testing.T, s graph.GraphStore) {
		mustEdge(t, s, "api", "billing")
		// Recorded api->billing; the incident names it billing->api.
		mustCausal(t, s, "INC-1", "billing", "api", "reversed")

		if e := edgeBetween(t, s, "api", "billing"); !e.IsCausal {
			t.Error("a reversed causal link did not match the edge")
		}
	})
}

// Two tenants running a service of the same name are separate services.
func TestConformanceTenantsAreIsolated(t *testing.T) {
	forEachStore(t, "tenant isolation", func(t *testing.T, s graph.GraphStore) {
		mustEdge(t, s, "api", "billing")
		if err := s.UpdateServiceState(ctx(), "other", "api", "DOWN"); err != nil {
			t.Fatalf("other tenant write: %v", err)
		}

		if got := nodeNamed(t, s, "api").State; got != "HEALTHY" {
			t.Errorf("another tenant's state bled through: %q", got)
		}
		other, err := s.GetGraph(ctx(), "other")
		if err != nil {
			t.Fatalf("other graph: %v", err)
		}
		if len(other.Nodes) != 1 || len(other.Edges) != 0 {
			t.Errorf("other tenant sees this tenant's topology: %+v", other)
		}
	})
}

// Deleting a tenant removes its topology and nothing else.
func TestConformanceDeleteTenantIsScoped(t *testing.T) {
	forEachStore(t, "delete tenant", func(t *testing.T, s graph.GraphStore) {
		mustEdge(t, s, "api", "billing")
		if err := s.UpsertDependencyEdge(ctx(), "other", "api", "billing"); err != nil {
			t.Fatalf("other tenant edge: %v", err)
		}

		if err := s.DeleteTenant(ctx(), tenant); err != nil {
			t.Fatalf("delete: %v", err)
		}
		g, err := s.GetGraph(ctx(), tenant)
		if err != nil {
			t.Fatalf("get graph: %v", err)
		}
		if len(g.Nodes) != 0 || len(g.Edges) != 0 {
			t.Errorf("topology survived deletion: %+v", g)
		}
		survivor, err := s.GetGraph(ctx(), "other")
		if err != nil {
			t.Fatalf("other graph: %v", err)
		}
		if len(survivor.Nodes) != 2 || len(survivor.Edges) != 1 {
			t.Errorf("deleting one tenant took another's topology: %+v", survivor)
		}
	})
}

// An empty tenant is refused rather than treated as a tenant named "".
//
// It would otherwise address the rows of a tenant literally named "" — and
// DeleteTenant would delete them.
func TestConformanceEmptyTenantIsRefused(t *testing.T) {
	forEachStore(t, "empty tenant", func(t *testing.T, s graph.GraphStore) {
		if err := s.UpsertDependencyEdge(ctx(), "", "api", "billing"); err == nil {
			t.Error("an empty tenant was accepted for a write")
		}
		if err := s.DeleteTenant(ctx(), ""); err == nil {
			t.Error("an empty tenant was accepted for a delete")
		}
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

func mustEdge(t *testing.T, s graph.GraphStore, from, to string) {
	t.Helper()
	if err := s.UpsertDependencyEdge(ctx(), tenant, from, to); err != nil {
		t.Fatalf("upsert edge %s->%s: %v", from, to, err)
	}
}

func mustCatalog(t *testing.T, s graph.GraphStore, name, team, repo, slack string) {
	t.Helper()
	if err := s.UpsertServiceCatalog(ctx(), tenant, name, team, repo, slack); err != nil {
		t.Fatalf("upsert catalog %s: %v", name, err)
	}
}

func mustCausal(t *testing.T, s graph.GraphStore, incident, source, target, reason string) {
	t.Helper()
	err := s.UpdateCausalPath(ctx(), tenant, incident, []graph.CausalLink{
		{Source: source, Target: target, Reason: reason},
	})
	if err != nil {
		t.Fatalf("causal path %s: %v", incident, err)
	}
}

func nodeNamed(t *testing.T, s graph.GraphStore, name string) graph.Node {
	t.Helper()
	g, err := s.GetGraph(ctx(), tenant)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	for _, n := range g.Nodes {
		if n.Id == name {
			return n
		}
	}
	t.Fatalf("no node %q in %+v", name, g.Nodes)
	return graph.Node{}
}

func edgeBetween(t *testing.T, s graph.GraphStore, source, target string) graph.Edge {
	t.Helper()
	g, err := s.GetGraph(ctx(), tenant)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	for _, e := range g.Edges {
		if e.Source == source && e.Target == target {
			return e
		}
	}
	t.Fatalf("no edge %s->%s in %+v", source, target, g.Edges)
	return graph.Edge{}
}
