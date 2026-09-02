package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	sharddb "github.com/pulsetrace/shared/db"
	_ "github.com/pulsetrace/shared/db/driver/sqlite"
	"github.com/pulsetrace/shared/graph/sqlstore"
)

// Tests for the caching decorator.
//
// The store underneath is the real SQL one on a temporary SQLite file, and the
// cache is a real Redis, because the thing under test is precisely the
// interaction between them: a fake of either would let a stale read look like a
// fresh one.
//
// Skipped without REDIS_ADDR. Caching is optional at runtime — the repository
// degrades to no cache when Redis is unreachable — so there is genuinely
// nothing to assert when it is absent, and saying so is better than a test that
// passes by doing nothing.

var repoSeq atomic.Uint64

func newTestRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("set REDIS_ADDR to exercise the topology cache (docker compose exposes 127.0.0.1:6379)")
	}

	conn, dialect, err := sharddb.Open(context.Background(), filepath.Join(t.TempDir(), "topo.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "001_create_graph_topology.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, stmt := range sharddb.ExpandStatements(string(raw)) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if err := sharddb.ExecStatement(context.Background(), conn, dialect, stmt); err != nil {
			t.Fatalf("apply migration: %v", err)
		}
	}

	repo := New(sqlstore.New(conn, dialect), addr)
	if repo.rdb == nil {
		t.Fatalf("REDIS_ADDR=%s is set but unreachable", addr)
	}

	// A tenant nobody else is using: Redis is shared and long-lived, unlike the
	// SQLite file, so a fixed name would inherit the last run's cache.
	tenant := fmt.Sprintf("repotest-%d", repoSeq.Add(1))
	t.Cleanup(func() { _ = repo.DeleteTenant(context.Background(), tenant) })
	return repo, tenant
}

// A newly recorded causal path must be visible immediately.
//
// Regression: UpdateCausalPath wrote through to the store but did not
// invalidate the cached graph, so a freshly highlighted path stayed invisible
// for up to the five-minute TTL. The incident view is the one place someone is
// waiting for this exact answer, and a blank causal path there reads as "the
// analysis found nothing" rather than "the cache has not caught up".
func TestCausalPathIsVisibleImmediately(t *testing.T) {
	repo, tenant := newTestRepo(t)
	ctx := context.Background()

	if err := repo.UpsertDependencyEdge(ctx, tenant, "api", "billing"); err != nil {
		t.Fatalf("upsert edge: %v", err)
	}
	// Warm the cache — this is what a user loading the topology screen does.
	if _, err := repo.GetGraph(ctx, tenant); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	if err := repo.UpdateCausalPath(ctx, tenant, "INC-1", []CausalLink{
		{Source: "api", Target: "billing", Reason: "latency spike"},
	}); err != nil {
		t.Fatalf("causal path: %v", err)
	}

	g, err := repo.GetGraph(ctx, tenant)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	if len(g.Edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(g.Edges))
	}
	if !g.Edges[0].IsCausal || !strings.Contains(g.Edges[0].Reason, "latency spike") {
		t.Errorf("the cached graph outlived the causal write: %+v", g.Edges[0])
	}
}

// Clearing a path must be equally immediate, or a re-analysis leaves a
// conclusion on screen that the engine has already withdrawn.
func TestClearedCausalPathDisappearsImmediately(t *testing.T) {
	repo, tenant := newTestRepo(t)
	ctx := context.Background()

	if err := repo.UpsertDependencyEdge(ctx, tenant, "api", "billing"); err != nil {
		t.Fatalf("upsert edge: %v", err)
	}
	if err := repo.UpdateCausalPath(ctx, tenant, "INC-1", []CausalLink{
		{Source: "api", Target: "billing", Reason: "first guess"},
	}); err != nil {
		t.Fatalf("causal path: %v", err)
	}
	if _, err := repo.GetGraph(ctx, tenant); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	// Re-analysed, implicating nothing.
	if err := repo.UpdateCausalPath(ctx, tenant, "INC-1", nil); err != nil {
		t.Fatalf("reanalyse: %v", err)
	}

	g, err := repo.GetGraph(ctx, tenant)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	if g.Edges[0].IsCausal {
		t.Errorf("a withdrawn conclusion survived in the cache: %+v", g.Edges[0])
	}
}
