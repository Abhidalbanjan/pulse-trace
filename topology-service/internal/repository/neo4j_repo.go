package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
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

// edgeMetricTTL bounds edge traffic metrics to a rolling recent window (Redis hash
// expires and resets) rather than an all-time total - a service map should show
// "is this edge busy/erroring right now," not a counter that only ever grows.
const edgeMetricTTL = 5 * time.Minute

func edgeMetricKey(from, to string) string {
	return "topo:edgemetric:" + from + "->" + to
}

// RecordEdgeMetric attributes one real request (with its actual duration and error
// status) to the from->to service edge, so the topology graph can show request
// rate/error rate/latency per dependency instead of a bare boolean "depends on."
func (r *Neo4jRepository) RecordEdgeMetric(ctx context.Context, from, to string, durationMs float64, isError bool) error {
	if r.rdb == nil {
		return nil
	}
	key := edgeMetricKey(from, to)
	pipe := r.rdb.Pipeline()
	pipe.HIncrBy(ctx, key, "count", 1)
	if isError {
		pipe.HIncrBy(ctx, key, "errors", 1)
	}
	pipe.HIncrByFloat(ctx, key, "duration_sum_ms", durationMs)
	pipe.Expire(ctx, key, edgeMetricTTL)
	_, err := pipe.Exec(ctx)
	return err
}

// EdgeMetric summarizes recent traffic over one service dependency edge.
type EdgeMetric struct {
	RequestCount int64
	ErrorCount   int64
	AvgLatencyMs float64
}

// GetEdgeMetric reads the current rolling-window metrics for one edge. Returns a
// zero-value metric (not an error) if the edge hasn't carried traffic recently -
// that's a legitimate state (e.g. right after startup, or Redis unavailable).
func (r *Neo4jRepository) GetEdgeMetric(ctx context.Context, from, to string) EdgeMetric {
	if r.rdb == nil {
		return EdgeMetric{}
	}
	vals, err := r.rdb.HGetAll(ctx, edgeMetricKey(from, to)).Result()
	if err != nil || len(vals) == 0 {
		return EdgeMetric{}
	}
	count, _ := strconv.ParseInt(vals["count"], 10, 64)
	errors, _ := strconv.ParseInt(vals["errors"], 10, 64)
	sum, _ := strconv.ParseFloat(vals["duration_sum_ms"], 64)
	avg := 0.0
	if count > 0 {
		avg = sum / float64(count)
	}
	return EdgeMetric{RequestCount: count, ErrorCount: errors, AvgLatencyMs: avg}
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

// UpsertServiceCatalog creates or updates catalog metadata for a service.
func (r *Neo4jRepository) UpsertServiceCatalog(ctx context.Context, serviceName, team, repo, slack string) error {
	query := `
		MERGE (s:Service {name: $serviceName})
		SET s.team = $team, s.repo = $repo, s.slack = $slack
	`
	_, err := neo4j.ExecuteQuery(ctx, r.driver, query, map[string]any{
		"serviceName": serviceName,
		"team":        team,
		"repo":        repo,
		"slack":       slack,
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
	Team  string `json:"team"`
	Repo  string `json:"repo"`
	Slack string `json:"slack"`
}

type Edge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	IsCausal bool   `json:"is_causal"`
	Reason   string `json:"reason"`
	// Recent traffic over this dependency (last edgeMetricTTL window), from
	// RecordEdgeMetric - lets the topology view distinguish a busy/erroring edge
	// from a quiet one instead of rendering every DEPENDS_ON line identically.
	RequestCount int64   `json:"request_count"`
	ErrorCount   int64   `json:"error_count"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
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

// UpdateCausalPath highlights the causal path for one specific incident. Each
// relationship stores its causal contributions as a list of "incidentID::reason"
// entries (causal_entries) rather than a single global is_causal/reason pair, so
// concurrent incidents analyzed at the same time never clobber each other's
// highlighting - re-running this for incidentID only ever touches entries that
// incident itself previously added.
func (r *Neo4jRepository) UpdateCausalPath(ctx context.Context, incidentID string, links []CausalLink) error {
	if incidentID == "" {
		return fmt.Errorf("incidentID is required")
	}

	// 1. Remove this incident's own prior contributions (e.g. a re-analysis that
	// found a shorter/different chain) - scoped by prefix match, never touches
	// entries belonging to any other incident.
	clearQuery := `
		MATCH ()-[r:DEPENDS_ON]->()
		WHERE size(coalesce(r.causal_entries, [])) > 0
		SET r.causal_entries = [x IN coalesce(r.causal_entries, []) WHERE NOT x STARTS WITH ($incidentID + '::')]
	`
	_, err := neo4j.ExecuteQuery(ctx, r.driver, clearQuery, map[string]any{"incidentID": incidentID}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	if err != nil {
		return err
	}

	// 2. Add this incident's new causal path links.
	setQuery := `
		MATCH (s:Service)-[r:DEPENDS_ON]->(t:Service)
		WHERE (s.name = $source AND t.name = $target) OR (s.name = $target AND t.name = $source)
		SET r.causal_entries = coalesce(r.causal_entries, []) + ($incidentID + '::' + $reason)
	`
	for _, link := range links {
		_, err := neo4j.ExecuteQuery(ctx, r.driver, setQuery, map[string]any{
			"source":     link.Source,
			"target":     link.Target,
			"reason":     link.Reason,
			"incidentID": incidentID,
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

	nodesRes, err := neo4j.ExecuteQuery(ctx, r.driver, `MATCH (n:Service) RETURN n.name AS id, n.state AS state, n.team AS team, n.repo AS repo, n.slack AS slack`, nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	if err != nil {
		return nil, err
	}
	var nodes []Node
	for _, record := range nodesRes.Records {
		id, _ := record.Get("id")
		state, _ := record.Get("state")
		team, _ := record.Get("team")
		repo, _ := record.Get("repo")
		slack, _ := record.Get("slack")

		s := "HEALTHY"
		if state != nil && state.(string) != "" {
			s = state.(string)
		}

		t := ""
		if team != nil && team.(string) != "" {
			t = team.(string)
		}
		rStr := ""
		if repo != nil && repo.(string) != "" {
			rStr = repo.(string)
		}
		sl := ""
		if slack != nil && slack.(string) != "" {
			sl = slack.(string)
		}

		nodes = append(nodes, Node{
			Id:    id.(string),
			State: s,
			Team:  t,
			Repo:  rStr,
			Slack: sl,
		})
	}

	edgesRes, err := neo4j.ExecuteQuery(ctx, r.driver, `MATCH (s:Service)-[r:DEPENDS_ON]->(t:Service) RETURN s.name AS source, t.name AS target, coalesce(r.causal_entries, []) AS causal_entries`, nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	if err != nil {
		return nil, err
	}
	var edges []Edge
	for _, record := range edgesRes.Records {
		source, _ := record.Get("source")
		target, _ := record.Get("target")
		entriesRaw, _ := record.Get("causal_entries")

		// causal_entries holds "incidentID::reason" strings from every incident
		// currently implicating this edge; join their reasons for display.
		var reasons []string
		if entries, ok := entriesRaw.([]interface{}); ok {
			for _, e := range entries {
				entry, ok := e.(string)
				if !ok {
					continue
				}
				if _, r, found := strings.Cut(entry, "::"); found {
					reasons = append(reasons, r)
				}
			}
		}

		sourceName := source.(string)
		targetName := target.(string)
		metric := r.GetEdgeMetric(ctx, sourceName, targetName)

		edges = append(edges, Edge{
			Source:       sourceName,
			Target:       targetName,
			IsCausal:     len(reasons) > 0,
			Reason:       strings.Join(reasons, "; "),
			RequestCount: metric.RequestCount,
			ErrorCount:   metric.ErrorCount,
			AvgLatencyMs: metric.AvgLatencyMs,
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

// AddPendingChild adds a child service call to the set waiting for a parent span,
// carrying the child span's own duration/error/id so a metric can still be
// attributed to the from->to edge once the parent arrives and resolves it (see
// ParsePendingChildEntry) - not just the bare service name.
func (r *Neo4jRepository) AddPendingChild(ctx context.Context, key, childService string, durationMs float64, isError bool, spanID string, ttl time.Duration) error {
	if r.rdb == nil {
		return nil
	}
	entry := fmt.Sprintf("%s|%.3f|%t|%s", childService, durationMs, isError, spanID)
	err := r.rdb.SAdd(ctx, key, entry).Err()
	if err != nil {
		return err
	}
	return r.rdb.Expire(ctx, key, ttl).Err()
}

// PendingChildEntry is one child call waiting on its parent span to resolve.
type PendingChildEntry struct {
	Service    string
	DurationMs float64
	IsError    bool
}

// ParsePendingChildEntry decodes an entry written by AddPendingChild.
func ParsePendingChildEntry(raw string) (PendingChildEntry, bool) {
	parts := strings.SplitN(raw, "|", 4)
	if len(parts) < 3 {
		return PendingChildEntry{}, false
	}
	durationMs, _ := strconv.ParseFloat(parts[1], 64)
	isError := parts[2] == "true"
	return PendingChildEntry{Service: parts[0], DurationMs: durationMs, IsError: isError}, true
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
