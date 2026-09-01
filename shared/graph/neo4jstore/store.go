// Package neo4jstore is the Neo4j implementation of the topology port (P1.4).
//
// # Why this is a separate package
//
// Same reason `shared/db/driver/sqlite` is: the binary chooses which engines it
// links. The Neo4j driver is a dependency the lite binary has no use for, and
// putting this in `shared/graph` would link it into every service that wants
// the port's *types*. Cluster services import this; lite imports sqlstore.
//
// # What moved and what did not
//
// The Cypher here is lifted from topology-service's repository unchanged — same
// queries, same MERGE keys, same tolerance for missing properties. What did not
// come across is the Redis half of that type: the read-through caches, the span
// buffer, and the edge metrics. Those are a different store with a different
// lifetime (a rolling five-minute window that expires), and the port has no
// business describing them. The repository keeps them and decorates what this
// returns.
package neo4jstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/pulsetrace/shared/graph"
)

// Store implements graph.GraphStore against Neo4j.
type Store struct {
	driver neo4j.DriverWithContext
	// database is the Neo4j database name. Every query names it explicitly, as
	// the original did — an unnamed query goes to the server's default, which
	// is a deployment setting rather than something this code should inherit.
	database string
}

var _ graph.GraphStore = (*Store)(nil)

// New wraps an existing driver. The caller owns the driver's lifetime, because
// topology-service shares one across this and its own queries.
func New(driver neo4j.DriverWithContext) *Store {
	return &Store{driver: driver, database: "neo4j"}
}

// requireTenant fails closed on an empty tenant.
//
// Neo4j would happily match nodes whose tenant property is "", and DeleteTenant
// would then delete them. The original had no guard; both implementations do
// now, because the conformance suite asserts it of every store.
func requireTenant(tenant string) error {
	if strings.TrimSpace(tenant) == "" {
		return fmt.Errorf("graph/neo4jstore: refusing to operate with an empty tenant")
	}
	return nil
}

func (s *Store) exec(ctx context.Context, query string, params map[string]any) (neo4j.EagerResult, error) {
	res, err := neo4j.ExecuteQuery(ctx, s.driver, query, params,
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.database))
	if err != nil {
		return neo4j.EagerResult{}, err
	}
	return *res, nil
}

// ── Writes ───────────────────────────────────────────────────────────────────

func (s *Store) UpsertDependencyEdge(ctx context.Context, tenant, from, to string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	// Three MERGEs: the edge write is also how a node comes into existence.
	_, err := s.exec(ctx, `
		MERGE (f:Service {name: $from, tenant: $tenant})
		MERGE (t:Service {name: $to, tenant: $tenant})
		MERGE (f)-[:DEPENDS_ON]->(t)
	`, map[string]any{"from": from, "to": to, "tenant": tenant})
	return err
}

func (s *Store) UpdateServiceState(ctx context.Context, tenant, service, state string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	_, err := s.exec(ctx, `
		MERGE (s:Service {name: $service, tenant: $tenant})
		SET s.state = $state, s.updated_at = timestamp()
	`, map[string]any{"service": service, "state": state, "tenant": tenant})
	return err
}

func (s *Store) UpsertServiceCatalog(ctx context.Context, tenant, service, team, repo, slack string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	_, err := s.exec(ctx, `
		MERGE (s:Service {name: $service, tenant: $tenant})
		SET s.team = $team, s.repo = $repo, s.slack = $slack
	`, map[string]any{"service": service, "team": team, "repo": repo, "slack": slack, "tenant": tenant})
	return err
}

func (s *Store) UpsertServiceMetadata(ctx context.Context, tenant, service, tier, lifecycle string, links map[string]string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	// Links are a JSON string because Neo4j properties cannot hold nested maps.
	// The SQL adapter keeps the same encoding so a topology moved between the
	// two reads back identically.
	_, err := s.exec(ctx, `
		MERGE (s:Service {name: $service, tenant: $tenant})
		SET s.tier = $tier, s.lifecycle = $lifecycle, s.links = $links
	`, map[string]any{
		"service": service, "tier": tier, "lifecycle": lifecycle,
		"links": encodeLinks(links), "tenant": tenant,
	})
	return err
}

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

// ── Reads ────────────────────────────────────────────────────────────────────

func (s *Store) GetDownstreamDependencies(ctx context.Context, tenant, service string) ([]string, error) {
	if err := requireTenant(tenant); err != nil {
		return nil, err
	}
	// Downstream = who depends on this one, i.e. the arrow points *at* it.
	return s.names(ctx, `
		MATCH (upstream:Service {name: $service, tenant: $tenant})<-[:DEPENDS_ON]-(downstream:Service {tenant: $tenant})
		RETURN downstream.name AS name ORDER BY name
	`, tenant, service)
}

func (s *Store) GetUpstreamDependencies(ctx context.Context, tenant, service string) ([]string, error) {
	if err := requireTenant(tenant); err != nil {
		return nil, err
	}
	return s.names(ctx, `
		MATCH (downstream:Service {name: $service, tenant: $tenant})-[:DEPENDS_ON]->(upstream:Service {tenant: $tenant})
		RETURN upstream.name AS name ORDER BY name
	`, tenant, service)
}

func (s *Store) names(ctx context.Context, query, tenant, service string) ([]string, error) {
	res, err := s.exec(ctx, query, map[string]any{"service": service, "tenant": tenant})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rec := range res.Records {
		if n := strProp(rec, "name"); n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *Store) GetServiceState(ctx context.Context, tenant, service string) (string, error) {
	if err := requireTenant(tenant); err != nil {
		return "", err
	}
	res, err := s.exec(ctx, `
		MATCH (s:Service {name: $service, tenant: $tenant}) RETURN s.state AS state
	`, map[string]any{"service": service, "tenant": tenant})
	if err != nil {
		return "", err
	}
	if len(res.Records) == 0 {
		return "", nil // unknown service is not an error
	}
	return strProp(res.Records[0], "state"), nil
}

// strProp reads a string property, tolerating nil/missing — an unmanaged
// service carries none of these, and metadata must never fail a graph read.
func strProp(rec *neo4j.Record, key string) string {
	if v, ok := rec.Get(key); ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
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

func (s *Store) GetGraph(ctx context.Context, tenant string) (*graph.Graph, error) {
	if err := requireTenant(tenant); err != nil {
		return nil, err
	}
	out := &graph.Graph{}

	nodes, err := s.exec(ctx, `
		MATCH (n:Service {tenant: $tenant})
		RETURN n.name AS id, n.state AS state, n.team AS team, n.repo AS repo,
		       n.slack AS slack, n.tier AS tier, n.lifecycle AS lifecycle, n.links AS links
		ORDER BY id
	`, map[string]any{"tenant": tenant})
	if err != nil {
		return nil, err
	}
	for _, rec := range nodes.Records {
		state := strProp(rec, "state")
		if state == "" {
			// No signal is the healthy case; the UI has always rendered it so.
			state = "HEALTHY"
		}
		out.Nodes = append(out.Nodes, graph.Node{
			Id:        strProp(rec, "id"),
			State:     state,
			Team:      strProp(rec, "team"),
			Repo:      strProp(rec, "repo"),
			Slack:     strProp(rec, "slack"),
			Tier:      strProp(rec, "tier"),
			Lifecycle: strProp(rec, "lifecycle"),
			Links:     decodeLinks(strProp(rec, "links")),
		})
	}

	edges, err := s.exec(ctx, `
		MATCH (s:Service {tenant: $tenant})-[r:DEPENDS_ON]->(t:Service {tenant: $tenant})
		RETURN s.name AS source, t.name AS target, coalesce(r.causal_entries, []) AS causal_entries
		ORDER BY source, target
	`, map[string]any{"tenant": tenant})
	if err != nil {
		return nil, err
	}
	for _, rec := range edges.Records {
		reasons := reasonsOf(entriesOf(rec))
		out.Edges = append(out.Edges, graph.Edge{
			Source:   strProp(rec, "source"),
			Target:   strProp(rec, "target"),
			IsCausal: len(reasons) > 0,
			Reason:   strings.Join(reasons, "; "),
			// Metrics stay zero: they are Redis's, not the graph's.
		})
	}
	return out, nil
}

func entriesOf(rec *neo4j.Record) []string {
	raw, _ := rec.Get("causal_entries")
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
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

func (s *Store) DeleteTenant(ctx context.Context, tenant string) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	_, err := s.exec(ctx, `MATCH (n:Service {tenant: $tenant}) DETACH DELETE n`,
		map[string]any{"tenant": tenant})
	return err
}

// ── Causal path ──────────────────────────────────────────────────────────────

func (s *Store) UpdateCausalPath(ctx context.Context, tenant, incidentID string, links []graph.CausalLink) error {
	if err := requireTenant(tenant); err != nil {
		return err
	}
	if incidentID == "" {
		return fmt.Errorf("graph/neo4jstore: incidentID is required")
	}

	// 1. Drop this incident's prior contributions, so a re-analysis that found a
	// shorter chain does not leave the old one highlighted.
	if _, err := s.exec(ctx, `
		MATCH (:Service {tenant: $tenant})-[r:DEPENDS_ON]->(:Service {tenant: $tenant})
		WHERE size(coalesce(r.causal_entries, [])) > 0
		SET r.causal_entries = [x IN coalesce(r.causal_entries, []) WHERE NOT x STARTS WITH ($incidentID + '::')]
	`, map[string]any{"incidentID": incidentID, "tenant": tenant}); err != nil {
		return err
	}

	// 2. Add the new ones. Direction is not significant.
	for _, link := range links {
		if _, err := s.exec(ctx, `
			MATCH (s:Service {tenant: $tenant})-[r:DEPENDS_ON]->(t:Service {tenant: $tenant})
			WHERE (s.name = $source AND t.name = $target) OR (s.name = $target AND t.name = $source)
			SET r.causal_entries = coalesce(r.causal_entries, []) + ($incidentID + '::' + $reason)
		`, map[string]any{
			"source": link.Source, "target": link.Target,
			"reason": link.Reason, "incidentID": incidentID, "tenant": tenant,
		}); err != nil {
			return err
		}
	}
	return nil
}
