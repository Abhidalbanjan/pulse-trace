// Package graph is the topology store port (P1.4).
//
// # Why this exists
//
// The service map lives in Neo4j, which is a container the cluster runs and a
// single-binary deployment cannot. Neo4j is one of the twenty-three containers
// D1 counts, and the Cypher behind it turns out to be shallow: ten queries,
// none of them traversals, all of them expressible in ordinary SQL.
//
// So the graph moves behind a port. The Neo4j adapter keeps the cluster
// behaviour it has today; the SQL adapter lets lite run the same topology on
// the database it already has.
//
// # What is deliberately not here
//
// **No Walk / variable-depth traversal.** The plan sketches
// `Walk(ctx, tenant, root, direction, maxDepth) ([]Path, error)` and gates the
// slice on an equivalence test over "cycles and diamond dependencies". There is
// nothing to be equivalent *to*: every Cypher statement in the service is
// depth-1 (`MATCH (a)-[:DEPENDS_ON]->(b)`) or a whole-graph scan, and no depth
// cap or cycle handling exists today. Adding a traversal here would be a new
// feature wearing a port's clothes, gated by a test comparing the SQL against a
// Cypher implementation written purely to be compared against. When something
// needs multi-hop, it gets its own slice and an honest gate.
//
// **No edge metrics.** `Edge.RequestCount` and friends come from Redis, not
// from the graph — a rolling five-minute window that expires, deliberately not
// a stored property. Putting them on this interface would make every
// implementation responsible for a store it has nothing to do with, so the
// graph is returned without them and the caller decorates. See EdgeMetric's
// absence from the interface below, and topology-service's repository wrapper.
//
// # Tenant isolation
//
// Every method takes the tenant explicitly and every implementation must scope
// on it. The Neo4j original keys each node on {name, tenant} so two tenants
// running a service of the same name get distinct nodes; the SQL adapter uses
// tenant_id as the leading column of both primary keys, which is the same
// guarantee expressed as a constraint the database enforces rather than a
// property each query has to remember.
package graph

import "context"

// Node is one service in a tenant's topology.
type Node struct {
	Id    string `json:"id"`
	State string `json:"state"`
	Team  string `json:"team"`
	Repo  string `json:"repo"`
	Slack string `json:"slack"`
	// Rich catalog metadata (Catalog · E3). Tier drives blast-radius
	// prioritisation; Lifecycle (experimental|production|deprecated) signals
	// maturity; Links holds structured pointers an on-call reaches for first.
	// All optional — an unmanaged service carries zero values.
	Tier      string            `json:"tier,omitempty"`
	Lifecycle string            `json:"lifecycle,omitempty"`
	Links     map[string]string `json:"links,omitempty"`
}

// Edge is one DEPENDS_ON dependency.
//
// RequestCount/ErrorCount/AvgLatencyMs are populated by the caller from the
// metric store, not by a GraphStore — see the package doc. A GraphStore leaves
// them zero.
type Edge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	IsCausal bool   `json:"is_causal"`
	Reason   string `json:"reason"`

	RequestCount int64   `json:"request_count"`
	ErrorCount   int64   `json:"error_count"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// Graph is one tenant's whole topology.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// CausalLink is one edge implicated in an incident's causal path.
type CausalLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// GraphStore is the topology port.
//
// The method set is the Cypher that exists, not a design: each one replaces a
// query in topology-service's Neo4j repository, with the same semantics
// including the ones that are surprising. Where a method's behaviour is
// load-bearing and non-obvious it is stated here, because "the same as Neo4j"
// stops being a specification the moment there is a second implementation.
type GraphStore interface {
	// UpsertDependencyEdge records that `from` depends on `to`.
	//
	// Creates either service if absent: the Cypher is three MERGEs, so an edge
	// write is also how nodes come into existence. A service that is only ever
	// a dependency target still appears in the graph.
	UpsertDependencyEdge(ctx context.Context, tenant, from, to string) error

	// UpdateServiceState sets a service's state, creating it if absent.
	UpdateServiceState(ctx context.Context, tenant, service, state string) error

	// UpsertServiceCatalog sets team/repo/slack, creating the service if absent.
	//
	// Deliberately distinct from UpsertServiceMetadata: they are two edit
	// surfaces over disjoint fields, and merging them would let one screen's
	// save clobber the other's fields with zero values.
	UpsertServiceCatalog(ctx context.Context, tenant, service, team, repo, slack string) error

	// UpsertServiceMetadata sets tier/lifecycle/links, creating the service if
	// absent. Links is stored encoded; an empty map clears it.
	UpsertServiceMetadata(ctx context.Context, tenant, service, tier, lifecycle string, links map[string]string) error

	// GetDownstreamDependencies returns the services that depend on this one.
	GetDownstreamDependencies(ctx context.Context, tenant, service string) ([]string, error)

	// GetUpstreamDependencies returns the services this one depends on.
	GetUpstreamDependencies(ctx context.Context, tenant, service string) ([]string, error)

	// GetServiceState returns a service's state, or "" when the service is
	// unknown or has no state. A missing service is not an error: callers ask
	// about services that may not have been seen yet.
	GetServiceState(ctx context.Context, tenant, service string) (string, error)

	// UpdateCausalPath records which edges one incident implicates.
	//
	// Each edge stores a list of "incidentID::reason" entries rather than a
	// single is_causal/reason pair, so incidents analysed concurrently do not
	// clobber each other. This first clears the incident's own prior entries —
	// a re-analysis that found a shorter chain must not leave the old one
	// highlighted — then adds the new ones.
	//
	// Link direction is not significant: an incident implicating A→B highlights
	// the edge whichever way round it was recorded, matching the Cypher's
	// `(s.name = $source AND t.name = $target) OR (s.name = $target AND ...)`.
	UpdateCausalPath(ctx context.Context, tenant, incidentID string, links []CausalLink) error

	// GetGraph returns one tenant's whole topology, edges carrying the joined
	// reasons of every incident currently implicating them. Edge metrics are
	// left zero; see the package doc.
	//
	// A node with no state reads back as "HEALTHY" rather than "": the absence
	// of a signal is the healthy case, and the UI has always rendered it so.
	GetGraph(ctx context.Context, tenant string) (*Graph, error)

	// DeleteTenant removes a tenant's entire topology, nodes and edges, for
	// offboarding and data-deletion requests.
	DeleteTenant(ctx context.Context, tenant string) error
}
