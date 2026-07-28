package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/pulsetrace/gateway-service/migrations"
	"github.com/pulsetrace/shared/migrate"
)

// newSavedSearchTestDB runs the real gateway migrations into a throwaway schema
// and returns a handler wired to it. It skips (not fails) when DATABASE_URL is
// unset, matching the other DB-backed tests in this repo, so CI without a
// Postgres service stays green.
func newSavedSearchTestDB(t *testing.T) (*SavedSearchHandler, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping saved-search DB test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1) // pin one conn so search_path sticks

	schema := fmt.Sprintf("ss_test_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DROP SCHEMA " + schema + " CASCADE")
		db.Close()
	})
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if err := migrate.Run(context.Background(), db, "ss_test", migrations.FS); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return NewSavedSearchHandler(db), db
}

// req builds an authenticated request for the given user/tenant. The gateway
// sets these headers from signed JWT claims, so the handler trusts them.
func ssReq(method, target, tenant, subject string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, target, &buf)
	r.Header.Set("X-Tenant-ID", tenant)
	r.Header.Set("X-User-Subject", subject)
	return r
}

func TestSavedSearch_CreateAndListOwn(t *testing.T) {
	h, _ := newSavedSearchTestDB(t)

	rr := httptest.NewRecorder()
	h.Create(rr, ssReq(http.MethodPost, "/api/v1/saved-searches", "acme", "alice", map[string]any{
		"name":         "errors last hour",
		"kind":         "logs",
		"query_params": map[string]string{"level": "error", "since": "1h"},
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.List(rr, ssReq(http.MethodGet, "/api/v1/saved-searches", "acme", "alice", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	var resp struct {
		Data []savedSearchRow `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 saved search, got %d", len(resp.Data))
	}
	got := resp.Data[0]
	if got.Name != "errors last hour" || got.QueryParams["since"] != "1h" || !got.Mine {
		t.Fatalf("row not persisted/echoed correctly: %+v", got)
	}
}

func TestSavedSearch_PrivateNotVisibleToOthers(t *testing.T) {
	h, _ := newSavedSearchTestDB(t)

	// alice creates a private (unshared) search.
	rr := httptest.NewRecorder()
	h.Create(rr, ssReq(http.MethodPost, "/api/v1/saved-searches", "acme", "alice", map[string]any{
		"name": "alice private", "kind": "logs",
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// bob in the same tenant must not see it.
	rr = httptest.NewRecorder()
	h.List(rr, ssReq(http.MethodGet, "/api/v1/saved-searches", "acme", "bob", nil))
	var resp struct {
		Data []savedSearchRow `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 0 {
		t.Fatalf("bob should not see alice's private search, got %d rows", len(resp.Data))
	}
}

func TestSavedSearch_SharedVisibleToTenantButNotEditable(t *testing.T) {
	h, db := newSavedSearchTestDB(t)

	shared := true
	rr := httptest.NewRecorder()
	h.Create(rr, ssReq(http.MethodPost, "/api/v1/saved-searches", "acme", "alice", map[string]any{
		"name": "team view", "kind": "logs", "shared": &shared,
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// bob sees it (shared), but it isn't marked as his.
	rr = httptest.NewRecorder()
	h.List(rr, ssReq(http.MethodGet, "/api/v1/saved-searches", "acme", "bob", nil))
	var resp struct {
		Data []savedSearchRow `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("bob should see the shared search, got %d", len(resp.Data))
	}
	if resp.Data[0].Mine {
		t.Fatal("shared search from alice must not be marked as bob's")
	}
	id := resp.Data[0].ID

	// bob tries to delete alice's shared search — ownership must block it.
	rr = httptest.NewRecorder()
	r := ssReq(http.MethodDelete, "/api/v1/saved-searches/"+id, "acme", "bob", nil)
	r.SetPathValue("id", id)
	h.Delete(rr, r)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("bob deleting alice's search should 404 (ownership), got %d", rr.Code)
	}

	// The row must still exist.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM saved_searches WHERE id = $1", id).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("shared search must survive a non-owner's delete, count=%d", count)
	}
}

func TestSavedSearch_CrossTenantIsolation(t *testing.T) {
	h, _ := newSavedSearchTestDB(t)

	// Same user id, two tenants — must not leak across the tenant boundary even
	// when shared.
	shared := true
	rr := httptest.NewRecorder()
	h.Create(rr, ssReq(http.MethodPost, "/api/v1/saved-searches", "tenant-a", "alice", map[string]any{
		"name": "a view", "shared": &shared,
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.List(rr, ssReq(http.MethodGet, "/api/v1/saved-searches", "tenant-b", "alice", nil))
	var resp struct {
		Data []savedSearchRow `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 0 {
		t.Fatalf("tenant-b must not see tenant-a's search, got %d rows", len(resp.Data))
	}
}

func TestSavedSearch_DuplicateNameConflicts(t *testing.T) {
	h, _ := newSavedSearchTestDB(t)
	body := map[string]any{"name": "dupe", "kind": "logs"}

	rr := httptest.NewRecorder()
	h.Create(rr, ssReq(http.MethodPost, "/api/v1/saved-searches", "acme", "alice", body))
	if rr.Code != http.StatusCreated {
		t.Fatalf("first create status = %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.Create(rr, ssReq(http.MethodPost, "/api/v1/saved-searches", "acme", "alice", body))
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate name should 409, got %d", rr.Code)
	}
}

func TestSavedSearch_KindFilter(t *testing.T) {
	h, _ := newSavedSearchTestDB(t)

	for _, s := range []map[string]any{
		{"name": "log view", "kind": "logs"},
		{"name": "trace view", "kind": "traces"},
	} {
		rr := httptest.NewRecorder()
		h.Create(rr, ssReq(http.MethodPost, "/api/v1/saved-searches", "acme", "alice", s))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %v status = %d", s["name"], rr.Code)
		}
	}

	rr := httptest.NewRecorder()
	h.List(rr, ssReq(http.MethodGet, "/api/v1/saved-searches?kind=traces", "acme", "alice", nil))
	var resp struct {
		Data []savedSearchRow `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Name != "trace view" {
		t.Fatalf("kind=traces should return only the trace view, got %+v", resp.Data)
	}

	// An unknown kind is a 400, not a silent empty result.
	rr = httptest.NewRecorder()
	h.List(rr, ssReq(http.MethodGet, "/api/v1/saved-searches?kind=metrics", "acme", "alice", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind filter should 400, got %d", rr.Code)
	}
}

func TestSavedSearch_InvalidKindRejected(t *testing.T) {
	h, _ := newSavedSearchTestDB(t)
	rr := httptest.NewRecorder()
	h.Create(rr, ssReq(http.MethodPost, "/api/v1/saved-searches", "acme", "alice", map[string]any{
		"name": "bad", "kind": "metrics",
	}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind should 400, got %d", rr.Code)
	}
}
