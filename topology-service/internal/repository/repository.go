// Package repository is topology-service's storage layer.
//
// # What changed in P1.4, and what did not
//
// This used to be `Neo4jRepository`, and it was two stores wearing one name: the
// service graph in Neo4j, and a Redis half doing read-through caching, span
// buffering and edge metrics. The graph moved behind `shared/graph` so a lite
// deployment can hold it in SQL; the Redis half stayed here, because it is not
// a graph and the port has no business describing it.
//
// So the type is now a decorator, and its job is the part that was always
// specific to this service: cache what the store returns, invalidate on write,
// and paint edge metrics onto a graph the store knows nothing about.
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

	"github.com/pulsetrace/shared/graph"
	"github.com/pulsetrace/shared/graph/neo4jstore"
)

// The domain types are the port's, aliased rather than redeclared.
//
// Aliases and not copies: api.go serialises these straight to JSON, and a
// parallel set of identical structs would be one rename away from a response
// that quietly changes shape.
type (
	Node       = graph.Node
	Edge       = graph.Edge
	Graph      = graph.Graph
	CausalLink = graph.CausalLink
)

// Repository is the service graph plus the Redis-backed state around it.
type Repository struct {
	store graph.GraphStore
	rdb   *redis.Client
	// closeStore releases whatever the store holds — a Bolt driver for Neo4j,
	// nothing for SQL, whose connection the caller owns.
	closeStore func(context.Context) error
}

// NewNeo4j builds the cluster configuration: the graph in Neo4j, state in Redis.
func NewNeo4j(uri, username, password, redisAddr string) (*Repository, error) {
	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(username, password, ""))
	if err != nil {
		return nil, err
	}
	return &Repository{
		store:      neo4jstore.New(driver),
		rdb:        openRedis(redisAddr),
		closeStore: driver.Close,
	}, nil
}

// New builds a repository over any store — the seam a lite deployment enters
// through with sqlstore, and the one tests enter through with a fake.
func New(store graph.GraphStore, redisAddr string) *Repository {
	return &Repository{store: store, rdb: openRedis(redisAddr)}
}

// openRedis connects if an address was given, and degrades to no caching rather
// than failing: the graph is authoritative and Redis is an accelerator, so an
// unreachable cache must not take the service down with it.
func openRedis(addr string) *redis.Client {
	if addr == "" {
		return nil
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		log.Printf("WARNING: failed to ping redis at %s: %v. Caching disabled.", addr, err)
		return nil
	}
	log.Printf("Connected to Redis for topology cache: %s", addr)
	return rdb
}

func (r *Repository) Close(ctx context.Context) error {
	if r.rdb != nil {
		r.rdb.Close()
	}
	if r.closeStore != nil {
		return r.closeStore(ctx)
	}
	return nil
}

// invalidateCache drops a tenant's cached views of one service. All keys are
// tenant-namespaced, so an invalidation never crosses tenants.
func (r *Repository) invalidateCache(ctx context.Context, tenant, service string) {
	if r.rdb == nil {
		return
	}
	keys := []string{
		"topo:upstream:" + tenant + ":" + service,
		"topo:downstream:" + tenant + ":" + service,
		"topo:graph:" + tenant,
	}
	if err := r.rdb.Del(ctx, keys...).Err(); err != nil {
		log.Printf("failed to invalidate cache keys for service %s/%s: %v", tenant, service, err)
	}
}

// ── Graph writes ─────────────────────────────────────────────────────────────

func (r *Repository) UpsertDependencyEdge(ctx context.Context, tenant, from, to string) error {
	err := r.store.UpsertDependencyEdge(ctx, tenant, from, to)
	if err == nil {
		r.invalidateCache(ctx, tenant, from)
		r.invalidateCache(ctx, tenant, to)
	}
	return err
}

func (r *Repository) UpdateServiceState(ctx context.Context, tenant, serviceName, state string) error {
	err := r.store.UpdateServiceState(ctx, tenant, serviceName, state)
	if err == nil {
		r.invalidateCache(ctx, tenant, serviceName)
	}
	return err
}

func (r *Repository) UpsertServiceCatalog(ctx context.Context, tenant, serviceName, team, repo, slack string) error {
	err := r.store.UpsertServiceCatalog(ctx, tenant, serviceName, team, repo, slack)
	if err == nil {
		r.invalidateCache(ctx, tenant, serviceName)
	}
	return err
}

func (r *Repository) UpsertServiceMetadata(ctx context.Context, tenant, serviceName, tier, lifecycle string, links map[string]string) error {
	err := r.store.UpsertServiceMetadata(ctx, tenant, serviceName, tier, lifecycle, links)
	if err == nil {
		r.invalidateCache(ctx, tenant, serviceName)
	}
	return err
}

// UpdateCausalPath records an incident's causal edges.
//
// The cached graph is dropped afterwards. Without that, a freshly highlighted
// path stayed invisible for up to the cache TTL — and the incident view is the
// one place someone is waiting for this exact answer, where a blank causal path
// reads as "the analysis found nothing" rather than "the cache has not caught
// up". The withdrawal case is worse: a conclusion the engine has already
// retracted stays on screen.
//
// Only the graph key, because causal entries are an edge property and change
// neither the upstream nor the downstream name lists.
func (r *Repository) UpdateCausalPath(ctx context.Context, tenant, incidentID string, links []CausalLink) error {
	err := r.store.UpdateCausalPath(ctx, tenant, incidentID, links)
	if err == nil && r.rdb != nil {
		if e := r.rdb.Del(ctx, "topo:graph:"+tenant).Err(); e != nil {
			log.Printf("failed to invalidate cached graph for tenant %s: %v", tenant, e)
		}
	}
	return err
}

func (r *Repository) DeleteTenant(ctx context.Context, tenant string) error {
	err := r.store.DeleteTenant(ctx, tenant)
	if err == nil && r.rdb != nil {
		// Drop the tenant's cached graph/edge entries so nothing stale survives.
		if keys, e := r.rdb.Keys(ctx, "topo:*:"+tenant+"*").Result(); e == nil && len(keys) > 0 {
			r.rdb.Del(ctx, keys...)
		}
		r.rdb.Del(ctx, "topo:graph:"+tenant)
	}
	return err
}

// ── Graph reads ──────────────────────────────────────────────────────────────

func (r *Repository) GetDownstreamDependencies(ctx context.Context, tenant, serviceName string) ([]string, error) {
	return r.cachedNames(ctx, "topo:downstream:"+tenant+":"+serviceName, func() ([]string, error) {
		return r.store.GetDownstreamDependencies(ctx, tenant, serviceName)
	})
}

func (r *Repository) GetUpstreamDependencies(ctx context.Context, tenant, serviceName string) ([]string, error) {
	return r.cachedNames(ctx, "topo:upstream:"+tenant+":"+serviceName, func() ([]string, error) {
		return r.store.GetUpstreamDependencies(ctx, tenant, serviceName)
	})
}

// cachedNames is the read-through cache both dependency lookups had inline.
func (r *Repository) cachedNames(ctx context.Context, key string, load func() ([]string, error)) ([]string, error) {
	if r.rdb != nil {
		if val, err := r.rdb.Get(ctx, key).Result(); err == nil {
			var deps []string
			if err := json.Unmarshal([]byte(val), &deps); err == nil {
				return deps, nil
			}
		}
	}
	deps, err := load()
	if err != nil {
		return nil, err
	}
	if r.rdb != nil {
		if val, err := json.Marshal(deps); err == nil {
			r.rdb.Set(ctx, key, val, 5*time.Minute)
		}
	}
	return deps, nil
}

func (r *Repository) GetServiceState(ctx context.Context, tenant, serviceName string) (string, error) {
	return r.store.GetServiceState(ctx, tenant, serviceName)
}

// GetGraph returns the tenant's topology with edge metrics painted on.
//
// The metrics are this layer's contribution: the store returns edges with them
// zeroed, because a rolling five-minute Redis window is not something a graph
// backend should have to know about. Cached whole, as before.
func (r *Repository) GetGraph(ctx context.Context, tenant string) (*Graph, error) {
	cacheKey := "topo:graph:" + tenant
	if r.rdb != nil {
		if val, err := r.rdb.Get(ctx, cacheKey).Result(); err == nil {
			var cached Graph
			if err := json.Unmarshal([]byte(val), &cached); err == nil {
				return &cached, nil
			}
		}
	}

	g, err := r.store.GetGraph(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for i := range g.Edges {
		m := r.GetEdgeMetric(ctx, tenant, g.Edges[i].Source, g.Edges[i].Target)
		g.Edges[i].RequestCount = m.RequestCount
		g.Edges[i].ErrorCount = m.ErrorCount
		g.Edges[i].AvgLatencyMs = m.AvgLatencyMs
	}

	if r.rdb != nil {
		if val, err := json.Marshal(g); err == nil {
			r.rdb.Set(ctx, cacheKey, val, 5*time.Minute)
		}
	}
	return g, nil
}

// ── Edge metrics (Redis) ─────────────────────────────────────────────────────

// edgeMetricTTL bounds edge traffic metrics to a rolling recent window (the
// Redis hash expires and resets) rather than an all-time total — a service map
// should show "is this edge busy/erroring right now", not a counter that only
// ever grows.
const edgeMetricTTL = 5 * time.Minute

func edgeMetricKey(tenant, from, to string) string {
	return "topo:edgemetric:" + tenant + ":" + from + "->" + to
}

// RecordEdgeMetric attributes one real request (with its actual duration and
// error status) to the tenant's from->to edge, so the topology graph can show
// request rate/error rate/latency per dependency instead of a bare boolean.
func (r *Repository) RecordEdgeMetric(ctx context.Context, tenant, from, to string, durationMs float64, isError bool) error {
	if r.rdb == nil {
		return nil
	}
	key := edgeMetricKey(tenant, from, to)
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

// GetEdgeMetric reads the current rolling-window metrics for one tenant edge.
func (r *Repository) GetEdgeMetric(ctx context.Context, tenant, from, to string) EdgeMetric {
	if r.rdb == nil {
		return EdgeMetric{}
	}
	vals, err := r.rdb.HGetAll(ctx, edgeMetricKey(tenant, from, to)).Result()
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

// ── Span buffer (Redis) ──────────────────────────────────────────────────────

// SetSpanService caches a span ID to its service name mapping. Span IDs are
// globally unique per trace, and a trace belongs to a single tenant, so these
// span-resolution keys don't need tenant namespacing — the tenant is carried
// through to the edge writes (UpsertDependencyEdge/RecordEdgeMetric) by the caller.
func (r *Repository) SetSpanService(ctx context.Context, key, serviceName string, ttl time.Duration) error {
	if r.rdb == nil {
		return nil
	}
	return r.rdb.Set(ctx, key, serviceName, ttl).Err()
}

// GetSpanService retrieves a cached span ID to service name mapping.
func (r *Repository) GetSpanService(ctx context.Context, key string) (string, error) {
	if r.rdb == nil {
		return "", fmt.Errorf("redis client not initialized")
	}
	return r.rdb.Get(ctx, key).Result()
}

// AddPendingChild adds a child service call to the set waiting for a parent span,
// carrying the child span's own duration/error/id so a metric can still be
// attributed to the from->to edge once the parent arrives and resolves it (see
// ParsePendingChildEntry) - not just the bare service name.
func (r *Repository) AddPendingChild(ctx context.Context, key, childService string, durationMs float64, isError bool, spanID string, ttl time.Duration) error {
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
func (r *Repository) GetPendingChildren(ctx context.Context, key string) ([]string, error) {
	if r.rdb == nil {
		return nil, fmt.Errorf("redis client not initialized")
	}
	return r.rdb.SMembers(ctx, key).Result()
}

// DeleteKey deletes a key from Redis.
func (r *Repository) DeleteKey(ctx context.Context, key string) error {
	if r.rdb == nil {
		return nil
	}
	return r.rdb.Del(ctx, key).Err()
}
