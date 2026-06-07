package repository

import (
	"context"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type Neo4jRepository struct {
	driver neo4j.DriverWithContext
}

func NewNeo4jRepository(uri, username, password string) (*Neo4jRepository, error) {
	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(username, password, ""))
	if err != nil {
		return nil, err
	}
	return &Neo4jRepository{driver: driver}, nil
}

func (r *Neo4jRepository) Close(ctx context.Context) error {
	return r.driver.Close(ctx)
}

// UpsertDependencyEdge creates or updates a dependency between two services.
func (r *Neo4jRepository) UpsertDependencyEdge(ctx context.Context, from, to string) error {
	query := `
		MERGE (f:Service {name: $from})
		MERGE (t:Service {name: $to})
		MERGE (f)-[:DEPENDS_ON]->(t)
	`
	_, err := neo4j.ExecuteQuery(ctx, r.driver, query, map[string]any{
		"from": from,
		"to":   to,
	}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	return err
}

// GetDownstreamDependencies returns a list of services that depend on the given service.
func (r *Neo4jRepository) GetDownstreamDependencies(ctx context.Context, serviceName string) ([]string, error) {
	query := `
		MATCH (upstream:Service {name: $serviceName})<-[:DEPENDS_ON]-(downstream:Service)
		RETURN downstream.name AS name
	`
	result, err := neo4j.ExecuteQuery(ctx, r.driver, query, map[string]any{
		"serviceName": serviceName,
	}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))

	if err != nil {
		return nil, err
	}

	var deps []string
	for _, record := range result.Records {
		name, _ := record.Get("name")
		deps = append(deps, name.(string))
	}
	return deps, nil
}

// UpdateServiceState updates the state (e.g., PREDICTIVE_WARNING) of a service.
func (r *Neo4jRepository) UpdateServiceState(ctx context.Context, serviceName, state string) error {
	query := `
		MERGE (s:Service {name: $serviceName})
		SET s.state = $state, s.updated_at = timestamp()
	`
	_, err := neo4j.ExecuteQuery(ctx, r.driver, query, map[string]any{
		"serviceName": serviceName,
		"state":       state,
	}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	return err
}

// GetUpstreamDependencies returns a list of services that this service depends on.
func (r *Neo4jRepository) GetUpstreamDependencies(ctx context.Context, serviceName string) ([]string, error) {
	query := `
		MATCH (downstream:Service {name: $serviceName})-[:DEPENDS_ON]->(upstream:Service)
		RETURN upstream.name AS name
	`
	result, err := neo4j.ExecuteQuery(ctx, r.driver, query, map[string]any{
		"serviceName": serviceName,
	}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))

	if err != nil {
		return nil, err
	}

	var deps []string
	for _, record := range result.Records {
		name, _ := record.Get("name")
		deps = append(deps, name.(string))
	}
	return deps, nil
}

type Node struct {
	Id    string `json:"id"`
	State string `json:"state"`
}

type Edge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	IsCausal bool   `json:"is_causal"`
	Reason   string `json:"reason"`
}

type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

type CausalLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// UpdateCausalPath clears old causal path highlights and applies the new ones.
func (r *Neo4jRepository) UpdateCausalPath(ctx context.Context, links []CausalLink) error {
	// 1. Reset all active causal flags
	clearQuery := `
		MATCH ()-[r:DEPENDS_ON]->()
		SET r.is_causal = false, r.reason = ""
	`
	_, err := neo4j.ExecuteQuery(ctx, r.driver, clearQuery, nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	if err != nil {
		return err
	}

	// 2. Set new causal path links
	setQuery := `
		MATCH (s:Service)-[r:DEPENDS_ON]->(t:Service)
		WHERE (s.name = $source AND t.name = $target) OR (s.name = $target AND t.name = $source)
		SET r.is_causal = true, r.reason = $reason
	`
	for _, link := range links {
		_, err := neo4j.ExecuteQuery(ctx, r.driver, setQuery, map[string]any{
			"source": link.Source,
			"target": link.Target,
			"reason": link.Reason,
		}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
		if err != nil {
			return err
		}
	}
	return nil
}

// GetGraph returns all nodes and edges in the topology.
func (r *Neo4jRepository) GetGraph(ctx context.Context) (*Graph, error) {
	nodesRes, err := neo4j.ExecuteQuery(ctx, r.driver, `MATCH (n:Service) RETURN n.name AS id, n.state AS state`, nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	if err != nil {
		return nil, err
	}
	var nodes []Node
	for _, record := range nodesRes.Records {
		id, _ := record.Get("id")
		state, _ := record.Get("state")
		s := ""
		if state != nil {
			s = state.(string)
		} else {
			s = "HEALTHY"
		}
		nodes = append(nodes, Node{Id: id.(string), State: s})
	}

	edgesRes, err := neo4j.ExecuteQuery(ctx, r.driver, `MATCH (s:Service)-[r:DEPENDS_ON]->(t:Service) RETURN s.name AS source, t.name AS target, r.is_causal AS is_causal, r.reason AS reason`, nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	if err != nil {
		return nil, err
	}
	var edges []Edge
	for _, record := range edgesRes.Records {
		source, _ := record.Get("source")
		target, _ := record.Get("target")
		isCausal, _ := record.Get("is_causal")
		reason, _ := record.Get("reason")
		
		ic := false
		if isCausal != nil {
			ic = isCausal.(bool)
		}
		re := ""
		if reason != nil {
			re = reason.(string)
		}
		edges = append(edges, Edge{
			Source:   source.(string),
			Target:   target.(string),
			IsCausal: ic,
			Reason:   re,
		})
	}

	return &Graph{Nodes: nodes, Edges: edges}, nil
}
