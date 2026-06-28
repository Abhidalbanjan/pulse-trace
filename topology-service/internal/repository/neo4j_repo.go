package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/redis/go-redis/v9"
)

type Neo4jRepository struct {
	driver neo4j.DriverWithContext
	rdb    *redis.Client
}

func NewNeo4jRepository(uri, username, password, redisAddr string) (*Neo4jRepository, error) {
	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(username, password, ""))
	if err != nil {
		return nil, err
	}

	var rdb *redis.Client
	if redisAddr != "" {
		rdb = redis.NewClient(&redis.Options{
			Addr: redisAddr,
		})
		// Ping redis to check connectivity
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rdb.Ping(pingCtx).Err(); err != nil {
			log.Printf("WARNING: failed to ping redis at %s: %v. Caching disabled.", redisAddr, err)
			rdb = nil
		} else {
			log.Printf("Connected to Redis for topology cache: %s", redisAddr)
		}
	}

	return &Neo4jRepository{driver: driver, rdb: rdb}, nil
}

func (r *Neo4jRepository) Close(ctx context.Context) error {
	if r.rdb != nil {
		r.rdb.Close()
	}
	return r.driver.Close(ctx)
}

// Helper to invalidate cached topology data
func (r *Neo4jRepository) invalidateCache(ctx context.Context, service string) {
	if r.rdb == nil {
		return
	}
	keys := []string{
		"topo:upstream:" + service,
		"topo:downstream:" + service,
		"topo:graph",
	}
	if err := r.rdb.Del(ctx, keys...).Err(); err != nil {
		log.Printf("failed to invalidate cache keys for service %s: %v", service, err)
	}
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
	if err == nil {
		r.invalidateCache(ctx, from)
		r.invalidateCache(ctx, to)
	}
	return err
}

// GetDownstreamDependencies returns a list of services that depend on the given service.
func (r *Neo4jRepository) GetDownstreamDependencies(ctx context.Context, serviceName string) ([]string, error) {
	cacheKey := "topo:downstream:" + serviceName
	if r.rdb != nil {
		val, err := r.rdb.Get(ctx, cacheKey).Result()
		if err == nil {
			var deps []string
			if err := json.Unmarshal([]byte(val), &deps); err == nil {
				return deps, nil
			}
		}
	}

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

	if r.rdb != nil {
		if val, err := json.Marshal(deps); err == nil {
			r.rdb.Set(ctx, cacheKey, val, 5*time.Minute)
		}
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
	if err == nil {
		r.invalidateCache(ctx, serviceName)
	}
	return err
}

// GetUpstreamDependencies returns a list of services that this service depends on.
func (r *Neo4jRepository) GetUpstreamDependencies(ctx context.Context, serviceName string) ([]string, error) {
	cacheKey := "topo:upstream:" + serviceName
	if r.rdb != nil {
		val, err := r.rdb.Get(ctx, cacheKey).Result()
		if err == nil {
			var deps []string
			if err := json.Unmarshal([]byte(val), &deps); err == nil {
				return deps, nil
			}
		}
	}

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

	if r.rdb != nil {
		if val, err := json.Marshal(deps); err == nil {
			r.rdb.Set(ctx, cacheKey, val, 5*time.Minute)
		}
	}

	return deps, nil
}

// GetServiceState returns the current state of a given service.
func (r *Neo4jRepository) GetServiceState(ctx context.Context, serviceName string) (string, error) {
	query := `
		MATCH (s:Service {name: $serviceName})
		RETURN s.state AS state
	`
	result, err := neo4j.ExecuteQuery(ctx, r.driver, query, map[string]any{
		"serviceName": serviceName,
	}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))

	if err != nil {
		return "", err
	}

	if len(result.Records) == 0 {
		return "", nil // Service not found
	}

	state, _ := result.Records[0].Get("state")
	if state == nil {
		return "", nil // No state set
	}

	return state.(string), nil
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
	cacheKey := "topo:graph"
	if r.rdb != nil {
		val, err := r.rdb.Get(ctx, cacheKey).Result()
		if err == nil {
			var graph Graph
			if err := json.Unmarshal([]byte(val), &graph); err == nil {
				return &graph, nil
			}
		}
	}

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

	graph := &Graph{Nodes: nodes, Edges: edges}

	if r.rdb != nil {
		if val, err := json.Marshal(graph); err == nil {
			r.rdb.Set(ctx, cacheKey, val, 5*time.Minute)
		}
	}

	return graph, nil
}

// SetSpanService caches a span ID to its service name mapping.
func (r *Neo4jRepository) SetSpanService(ctx context.Context, key, serviceName string, ttl time.Duration) error {
	if r.rdb == nil {
		return nil
	}
	return r.rdb.Set(ctx, key, serviceName, ttl).Err()
}

// GetSpanService retrieves a cached span ID to service name mapping.
func (r *Neo4jRepository) GetSpanService(ctx context.Context, key string) (string, error) {
	if r.rdb == nil {
		return "", fmt.Errorf("redis client not initialized")
	}
	return r.rdb.Get(ctx, key).Result()
}

// AddPendingChild adds a service name to the set of child services waiting for a parent span.
func (r *Neo4jRepository) AddPendingChild(ctx context.Context, key, childService string, ttl time.Duration) error {
	if r.rdb == nil {
		return nil
	}
	err := r.rdb.SAdd(ctx, key, childService).Err()
	if err != nil {
		return err
	}
	return r.rdb.Expire(ctx, key, ttl).Err()
}

// GetPendingChildren retrieves all child services waiting for a parent span.
func (r *Neo4jRepository) GetPendingChildren(ctx context.Context, key string) ([]string, error) {
	if r.rdb == nil {
		return nil, fmt.Errorf("redis client not initialized")
	}
	return r.rdb.SMembers(ctx, key).Result()
}

// DeleteKey deletes a key from Redis.
func (r *Neo4jRepository) DeleteKey(ctx context.Context, key string) error {
	if r.rdb == nil {
		return nil
	}
	return r.rdb.Del(ctx, key).Err()
}
