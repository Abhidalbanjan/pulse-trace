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
// # Running it
//
// The SQL store always runs, against a temporary SQLite file with the real
// migration applied. Neo4j runs when NEO4J_CONFORMANCE=1, against NEO4J_URI
// (default bolt://127.0.0.1:7687) — the same skip-unless-configured shape the
// bus suite uses for Kafka, and for the same reason: a developer without the
// container still gets the contract checked, and CI with the container is what
// makes it prove the two agree.
//
// # Why every test invents its own tenants
//
// SQLite gets a fresh file per subtest; Neo4j does not — it is one long-lived
// database, and a fixed tenant name would make each run inherit the last one's
// topology. Tenants are therefore unique per subtest and deleted afterwards,
// which also means the suite can run against a Neo4j that has real data in it
// without touching any of it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	sharddb "github.com/pulsetrace/shared/db"
	_ "github.com/pulsetrace/shared/db/driver/sqlite"
	"github.com/pulsetrace/shared/graph"
	"github.com/pulsetrace/shared/graph/neo4jstore"
	"github.com/pulsetrace/shared/graph/sqlstore"
)

// storeFactory builds a store. Cleanup is registered on t.
type storeFactory struct {
	name string
	open func(t *testing.T) graph.GraphStore
}

func conformanceStores(t *testing.T) []storeFactory {
	t.Helper()
	stores := []storeFactory{{name: "sql/sqlite", open: openSQLiteStore}}
	if os.Getenv("NEO4J_CONFORMANCE") != "" {
		stores = append(stores, storeFactory{name: "neo4j", open: openNeo4jStore})
	}
	return stores
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

func openNeo4jStore(t *testing.T) graph.GraphStore {
	t.Helper()
	uri := envOr("NEO4J_URI", "bolt://127.0.0.1:7687")
	user := envOr("NEO4J_USERNAME", "neo4j")
	pass := envOr("NEO4J_PASSWORD", "pulsetrace_secret")

	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(user, pass, ""))
	if err != nil {
		t.Fatalf("neo4j driver: %v", err)
	}
	if err := driver.VerifyConnectivity(context.Background()); err != nil {
		// Configured but unreachable is a failure, not a skip: NEO4J_CONFORMANCE
		// was set on purpose, and silently proving nothing is the outcome this
		// suite exists to prevent.
		t.Fatalf("NEO4J_CONFORMANCE is set but %s is unreachable: %v", uri, err)
	}
	t.Cleanup(func() { driver.Close(context.Background()) })
	return neo4jstore.New(driver)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// env is one assertion's world: a store and two tenants nobody else is using.
type env struct {
	s      graph.GraphStore
	tenant string // the tenant under test
	other  string // a second tenant, for isolation assertions
}

var tenantSeq atomic.Uint64

// forEachStore runs one assertion against every implementation, with fresh
// tenants and a cleanup that removes them.
func forEachStore(t *testing.T, name string, fn func(t *testing.T, e env)) {
	t.Helper()
	for _, f := range conformanceStores(t) {
		t.Run(f.name+"/"+name, func(t *testing.T) {
			n := tenantSeq.Add(1)
			e := env{
				s:      f.open(t),
				tenant: fmt.Sprintf("conf-%d-a", n),
				other:  fmt.Sprintf("conf-%d-b", n),
			}
			t.Cleanup(func() {
				_ = e.s.DeleteTenant(context.Background(), e.tenant)
				_ = e.s.DeleteTenant(context.Background(), e.other)
			})
			fn(t, e)
		})
	}
}

func ctx() context.Context { return context.Background() }

// ── The contract ─────────────────────────────────────────────────────────────

// An edge write creates both endpoints.
//
// The Cypher is three MERGEs, so recording a dependency is also how a service
// comes into existence. A store that only wrote the edge would leave a graph
// whose nodes list is missing every service that is purely a dependency target.
func TestConformanceEdgeWriteCreatesBothServices(t *testing.T) {
	forEachStore(t, "edge creates nodes", func(t *testing.T, e env) {
		e.mustEdge(t, "api", "billing")

		g := e.graph(t)
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
	forEachStore(t, "edge preserves catalog", func(t *testing.T, e env) {
		e.mustCatalog(t, "api", "platform", "org/api", "#api")
		e.mustEdge(t, "api", "billing")

		n := e.node(t, "api")
		if n.Team != "platform" || n.Repo != "org/api" || n.Slack != "#api" {
			t.Errorf("catalog lost after an edge write: %+v", n)
		}
	})
}

// The two edit surfaces are disjoint and must not overwrite each other.
func TestConformanceCatalogAndMetadataDoNotClobber(t *testing.T) {
	forEachStore(t, "disjoint edit surfaces", func(t *testing.T, e env) {
		e.mustCatalog(t, "api", "platform", "org/api", "#api")
		if err := e.s.UpsertServiceMetadata(ctx(), e.tenant, "api", "tier-1", "production",
			map[string]string{"runbook": "https://rb"}); err != nil {
			t.Fatalf("upsert metadata: %v", err)
		}
		if err := e.s.UpdateServiceState(ctx(), e.tenant, "api", "DEGRADED"); err != nil {
			t.Fatalf("update state: %v", err)
		}

		n := e.node(t, "api")
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
	forEachStore(t, "directions", func(t *testing.T, e env) {
		// api -> billing -> ledger
		e.mustEdge(t, "api", "billing")
		e.mustEdge(t, "billing", "ledger")

		up, err := e.s.GetUpstreamDependencies(ctx(), e.tenant, "billing")
		if err != nil {
			t.Fatalf("upstream: %v", err)
		}
		if len(up) != 1 || up[0] != "ledger" {
			t.Errorf("upstream of billing = %v, want [ledger]", up)
		}

		down, err := e.s.GetDownstreamDependencies(ctx(), e.tenant, "billing")
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
	forEachStore(t, "unknown service", func(t *testing.T, e env) {
		state, err := e.s.GetServiceState(ctx(), e.tenant, "never-seen")
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
	forEachStore(t, "healthy default", func(t *testing.T, e env) {
		e.mustEdge(t, "api", "billing")
		if got := e.node(t, "api").State; got != "HEALTHY" {
			t.Errorf("state = %q, want HEALTHY", got)
		}
	})
}

// Two incidents may implicate the same edge, and neither may erase the other.
//
// This is why causal data is a list of "incidentID::reason" and not a scalar:
// two analyses running at once both legitimately touch the same dependency.
func TestConformanceConcurrentIncidentsShareAnEdge(t *testing.T) {
	forEachStore(t, "two incidents one edge", func(t *testing.T, e env) {
		e.mustEdge(t, "api", "billing")
		e.mustCausal(t, "INC-1", "api", "billing", "latency spike")
		e.mustCausal(t, "INC-2", "api", "billing", "error budget burn")

		edge := e.edge(t, "api", "billing")
		if !edge.IsCausal {
			t.Fatal("edge is not marked causal")
		}
		if !strings.Contains(edge.Reason, "latency spike") || !strings.Contains(edge.Reason, "error budget burn") {
			t.Errorf("one incident erased the other: %q", edge.Reason)
		}
	})
}

// Re-analysing an incident replaces its own entries and only its own.
func TestConformanceReanalysisClearsOnlyItsOwnEntries(t *testing.T) {
	forEachStore(t, "reanalysis", func(t *testing.T, e env) {
		e.mustEdge(t, "api", "billing")
		e.mustCausal(t, "INC-1", "api", "billing", "first guess")
		e.mustCausal(t, "INC-2", "api", "billing", "other incident")

		// INC-1 re-analysed, and this time it implicates nothing.
		if err := e.s.UpdateCausalPath(ctx(), e.tenant, "INC-1", nil); err != nil {
			t.Fatalf("reanalyse: %v", err)
		}

		edge := e.edge(t, "api", "billing")
		if strings.Contains(edge.Reason, "first guess") {
			t.Errorf("a stale conclusion survived re-analysis: %q", edge.Reason)
		}
		if !strings.Contains(edge.Reason, "other incident") {
			t.Errorf("re-analysing one incident cleared another's: %q", edge.Reason)
		}
	})
}

// Causal links match an edge whichever way round they are given.
func TestConformanceCausalLinkDirectionIsNotSignificant(t *testing.T) {
	forEachStore(t, "causal direction", func(t *testing.T, e env) {
		e.mustEdge(t, "api", "billing")
		// Recorded api->billing; the incident names it billing->api.
		e.mustCausal(t, "INC-1", "billing", "api", "reversed")

		if edge := e.edge(t, "api", "billing"); !edge.IsCausal {
			t.Error("a reversed causal link did not match the edge")
		}
	})
}

// Two tenants running a service of the same name are separate services.
func TestConformanceTenantsAreIsolated(t *testing.T) {
	forEachStore(t, "tenant isolation", func(t *testing.T, e env) {
		e.mustEdge(t, "api", "billing")
		if err := e.s.UpdateServiceState(ctx(), e.other, "api", "DOWN"); err != nil {
			t.Fatalf("other tenant write: %v", err)
		}

		if got := e.node(t, "api").State; got != "HEALTHY" {
			t.Errorf("another tenant's state bled through: %q", got)
		}
		other, err := e.s.GetGraph(ctx(), e.other)
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
	forEachStore(t, "delete tenant", func(t *testing.T, e env) {
		e.mustEdge(t, "api", "billing")
		if err := e.s.UpsertDependencyEdge(ctx(), e.other, "api", "billing"); err != nil {
			t.Fatalf("other tenant edge: %v", err)
		}

		if err := e.s.DeleteTenant(ctx(), e.tenant); err != nil {
			t.Fatalf("delete: %v", err)
		}
		g := e.graph(t)
		if len(g.Nodes) != 0 || len(g.Edges) != 0 {
			t.Errorf("topology survived deletion: %+v", g)
		}
		survivor, err := e.s.GetGraph(ctx(), e.other)
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
	forEachStore(t, "empty tenant", func(t *testing.T, e env) {
		if err := e.s.UpsertDependencyEdge(ctx(), "", "api", "billing"); err == nil {
			t.Error("an empty tenant was accepted for a write")
		}
		if err := e.s.DeleteTenant(ctx(), ""); err == nil {
			t.Error("an empty tenant was accepted for a delete")
		}
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (e env) mustEdge(t *testing.T, from, to string) {
	t.Helper()
	if err := e.s.UpsertDependencyEdge(ctx(), e.tenant, from, to); err != nil {
		t.Fatalf("upsert edge %s->%s: %v", from, to, err)
	}
}

func (e env) mustCatalog(t *testing.T, name, team, repo, slack string) {
	t.Helper()
	if err := e.s.UpsertServiceCatalog(ctx(), e.tenant, name, team, repo, slack); err != nil {
		t.Fatalf("upsert catalog %s: %v", name, err)
	}
}

func (e env) mustCausal(t *testing.T, incident, source, target, reason string) {
	t.Helper()
	err := e.s.UpdateCausalPath(ctx(), e.tenant, incident, []graph.CausalLink{
		{Source: source, Target: target, Reason: reason},
	})
	if err != nil {
		t.Fatalf("causal path %s: %v", incident, err)
	}
}

func (e env) graph(t *testing.T) *graph.Graph {
	t.Helper()
	g, err := e.s.GetGraph(ctx(), e.tenant)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	return g
}

func (e env) node(t *testing.T, name string) graph.Node {
	t.Helper()
	g := e.graph(t)
	for _, n := range g.Nodes {
		if n.Id == name {
			return n
		}
	}
	t.Fatalf("no node %q in %+v", name, g.Nodes)
	return graph.Node{}
}

func (e env) edge(t *testing.T, source, target string) graph.Edge {
	t.Helper()
	g := e.graph(t)
	for _, ed := range g.Edges {
		if ed.Source == source && ed.Target == target {
			return ed
		}
	}
	t.Fatalf("no edge %s->%s in %+v", source, target, g.Edges)
	return graph.Edge{}
}
