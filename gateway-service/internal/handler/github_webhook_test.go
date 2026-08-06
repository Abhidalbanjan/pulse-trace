package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	gatewaymigrations "github.com/pulsetrace/gateway-service/migrations"
	"github.com/pulsetrace/shared/migrate"
)

// evaluatorReturning is a stub for correlation-service's /api/v1/slo/evaluate-pr.
func evaluatorReturning(decision string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"decision": decision}})
	}))
}

func prEventBody(number int, title string) string {
	return fmt.Sprintf(`{"action":"opened","pull_request":{"number":%d,"title":%q,"user":{"login":"octocat"},"html_url":"http://gh/pr/%d","head":{"sha":"abc123"}},"repository":{"full_name":"acme/app"}}`, number, title, number)
}

func TestVerifySignature(t *testing.T) {
	h := &GithubWebhookHandler{webhookSecret: "s3cr3t"}
	body := []byte(prEventBody(1, "x"))
	mac := hmac.New(sha256.New, []byte("s3cr3t"))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !h.verifySignature(good, body) {
		t.Error("valid signature must pass")
	}
	if h.verifySignature("sha256=deadbeef", body) {
		t.Error("wrong signature must fail")
	}
	if h.verifySignature("", body) {
		t.Error("missing signature must fail when a secret is configured")
	}
	// No secret configured → accept (documented dev posture).
	open := &GithubWebhookHandler{}
	if !open.verifySignature("", body) {
		t.Error("with no secret configured, requests must be accepted")
	}
}

func setupGateDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed deploy-gate test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("gate_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE") })
	if _, err := db.Exec("SET search_path TO " + schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if err := migrate.Run(context.Background(), db, "gate_test", gatewaymigrations.FS); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return db
}

// TestGateBlockPersistsAndLists: a BLOCK verdict returns a GitHub "failure"
// status, records the gate, and surfaces it via ListGates.
func TestGateBlockPersistsAndLists(t *testing.T) {
	db := setupGateDB(t)
	eval := evaluatorReturning("BLOCK")
	defer eval.Close()
	h := NewGithubWebhookHandler(db, eval.URL)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(prEventBody(42, "risky migration")))
	req.Header.Set("X-GitHub-Event", "pull_request")
	rr := httptest.NewRecorder()
	h.Handle(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("BLOCK should return 403, got %d (%s)", rr.Code, rr.Body.String())
	}

	lr := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/gates", nil)
	lr.Header.Set("X-Tenant-ID", "default")
	lrr := httptest.NewRecorder()
	h.ListGates(lrr, lr)
	if lrr.Code != http.StatusOK {
		t.Fatalf("list gates status %d", lrr.Code)
	}
	var resp struct {
		Data []deployGateView `json:"data"`
	}
	if err := json.Unmarshal(lrr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].PRNumber != 42 || resp.Data[0].Decision != "BLOCK" {
		t.Fatalf("expected one recorded BLOCK gate for PR 42, got %+v", resp.Data)
	}
}

// TestGateFailsOpenWhenEvaluatorDown: if the evaluator is unreachable the gate
// approves (advisory, not a hard availability dependency) and still records it.
func TestGateFailsOpenWhenEvaluatorDown(t *testing.T) {
	db := setupGateDB(t)
	// Point at a closed server to force a transport error.
	down := evaluatorReturning("BLOCK")
	down.Close()
	h := NewGithubWebhookHandler(db, down.URL)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(prEventBody(7, "safe change")))
	req.Header.Set("X-GitHub-Event", "pull_request")
	rr := httptest.NewRecorder()
	h.Handle(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("evaluator-down should fail open (200), got %d", rr.Code)
	}
}
