// Package tenantdata deletes all of a tenant's data across every store, for
// GDPR/SOC2 "delete my data" and tenant offboarding. It is deliberately
// best-effort per store: one backend failing must not prevent the others from
// being purged, so it collects errors and continues rather than stopping at the
// first failure.
package tenantdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Purger deletes a tenant's data from ClickHouse (telemetry), Postgres
// (incidents/usage/config), Quickwit (log index) and Neo4j (topology, via the
// topology service).
type Purger struct {
	db            *sql.DB
	clickhouseURL string
	chUser        string
	chPass        string
	topologyURL   string
	quickwitURL   string
	quickwitIndex string
	client        *http.Client
}

func New(db *sql.DB, clickhouseURL, topologyURL, quickwitURL string) *Purger {
	return &Purger{
		db:            db,
		clickhouseURL: clickhouseURL,
		chUser:        envOr("CLICKHOUSE_USER", "pulsetrace"),
		chPass:        envOr("CLICKHOUSE_PASSWORD", "pulsetrace_secret"),
		topologyURL:   topologyURL,
		quickwitURL:   quickwitURL,
		quickwitIndex: "pulsetrace-logs",
		client:        &http.Client{Timeout: 30 * time.Second},
	}
}

// Result summarizes a purge: which stores succeeded and any errors encountered.
type Result struct {
	TenantID string   `json:"tenant_id"`
	Full     bool     `json:"full"`
	Steps    []string `json:"steps"`
	Errors   []string `json:"errors"`
}

func (r *Result) ok(step string)          { r.Steps = append(r.Steps, step) }
func (r *Result) fail(step string, e error) {
	r.Errors = append(r.Errors, fmt.Sprintf("%s: %v", step, e))
}

// PurgeTenant deletes the tenant's telemetry everywhere. When full is true it
// also deletes the account itself (users, keys, alert rules, and the tenant row).
func (p *Purger) PurgeTenant(ctx context.Context, tenantID string, full bool) *Result {
	res := &Result{TenantID: tenantID, Full: full}

	p.purgeClickHouse(ctx, tenantID, res)
	p.purgeQuickwit(ctx, tenantID, res)
	p.purgeTopology(ctx, tenantID, res)
	p.purgePostgres(ctx, tenantID, full, res)

	return res
}

// ── ClickHouse (hot telemetry) ────────────────────────────────────────────────

func (p *Purger) purgeClickHouse(ctx context.Context, tenantID string, res *Result) {
	if p.clickhouseURL == "" {
		return
	}
	q := chQuote(tenantID)
	// rum_events / synthetic_results are PARTITION BY TenantID — dropping the
	// partition is a near-instant metadata operation.
	for _, table := range []string{"pulsetrace.rum_events", "pulsetrace.synthetic_results"} {
		stmt := fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", table, q)
		if err := p.clickhouseExec(ctx, stmt); err != nil {
			res.fail("clickhouse drop partition "+table, err)
		} else {
			res.ok("clickhouse " + table)
		}
	}
	// otel_traces / otel_metrics_* carry the tenant as a resource attribute, not a
	// partition, so use a lightweight mutation.
	for _, table := range []string{"pulsetrace.otel_traces", "pulsetrace.otel_metrics_gauge", "pulsetrace.otel_metrics_sum"} {
		stmt := fmt.Sprintf("ALTER TABLE %s DELETE WHERE ResourceAttributes['tenant.id'] = %s", table, q)
		if err := p.clickhouseExec(ctx, stmt); err != nil {
			res.fail("clickhouse delete "+table, err)
		} else {
			res.ok("clickhouse " + table)
		}
	}
}

func (p *Purger) clickhouseExec(ctx context.Context, stmt string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.clickhouseURL, strings.NewReader(stmt))
	if err != nil {
		return err
	}
	req.SetBasicAuth(p.chUser, p.chPass)
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("clickhouse %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// ── Quickwit (log index) ──────────────────────────────────────────────────────

func (p *Purger) purgeQuickwit(ctx context.Context, tenantID string, res *Result) {
	if p.quickwitURL == "" {
		return
	}
	url := fmt.Sprintf("%s/api/v1/%s/delete-tasks", strings.TrimRight(p.quickwitURL, "/"), p.quickwitIndex)
	body := fmt.Sprintf(`{"query":"tenant_id:%s"}`, tenantID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		res.fail("quickwit delete", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		res.fail("quickwit delete", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		res.fail("quickwit delete", fmt.Errorf("%d: %s", resp.StatusCode, strings.TrimSpace(string(b))))
		return
	}
	res.ok("quickwit logs")
}

// ── Neo4j topology (via topology service) ─────────────────────────────────────

func (p *Purger) purgeTopology(ctx context.Context, tenantID string, res *Result) {
	if p.topologyURL == "" {
		return
	}
	url := strings.TrimRight(p.topologyURL, "/") + "/api/v1/topology/tenant"
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		res.fail("topology delete", err)
		return
	}
	req.Header.Set("X-Tenant-ID", tenantID)
	resp, err := p.client.Do(req)
	if err != nil {
		res.fail("topology delete", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		res.fail("topology delete", fmt.Errorf("status %d", resp.StatusCode))
		return
	}
	res.ok("neo4j topology")
}

// ── Postgres ──────────────────────────────────────────────────────────────────

func (p *Purger) purgePostgres(ctx context.Context, tenantID string, full bool, res *Result) {
	if p.db == nil {
		return
	}
	// Telemetry-derived rows first (always deleted). incident_alerts is a child of
	// incidents, so clear it via the parent's tenant before deleting incidents.
	steps := []struct {
		name string
		stmt string
	}{
		{"incident_alerts", "DELETE FROM incident_alerts WHERE incident_id IN (SELECT id FROM incidents WHERE tenant_id = $1)"},
		{"incidents", "DELETE FROM incidents WHERE tenant_id = $1"},
		{"alerts", "DELETE FROM alerts WHERE tenant_id = $1"},
		{"deployments", "DELETE FROM deployments WHERE tenant_id = $1"},
		{"synthetic_targets", "DELETE FROM synthetic_targets WHERE tenant_id = $1"},
		{"usage_daily", "DELETE FROM usage_daily WHERE tenant_id = $1"},
	}
	if full {
		// Account teardown: config + identity + the tenant row itself.
		steps = append(steps,
			struct{ name, stmt string }{"alert_rules", "DELETE FROM alert_rules WHERE tenant_id = $1"},
			struct{ name, stmt string }{"ingestion_keys", "DELETE FROM ingestion_keys WHERE tenant_id = $1"},
			struct{ name, stmt string }{"users", "DELETE FROM users WHERE tenant_id = $1"},
			struct{ name, stmt string }{"tenants", "DELETE FROM tenants WHERE id = $1"},
		)
	}

	for _, s := range steps {
		if _, err := p.db.ExecContext(ctx, s.stmt, tenantID); err != nil {
			// A missing table (e.g. correlation migrations not applied here) is
			// non-fatal — record it and keep going.
			res.fail("postgres "+s.name, err)
		} else {
			res.ok("postgres " + s.name)
		}
	}
}

// chQuote renders a ClickHouse string literal, escaping single quotes. Tenant
// slugs are [a-z0-9-] so this is belt-and-suspenders against injection.
func chQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

func tenantOf(r *http.Request) string {
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return "default"
}

// requireConfirmation guards these destructive endpoints: the body must contain
// {"confirm":"<tenant_id>"} matching the caller's own tenant, so a stray POST
// can't wipe data. Returns false (and writes the error) if it doesn't match.
func requireConfirmation(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	var body struct {
		Confirm string `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Confirm != tenantID {
		http.Error(w, `confirmation required: send {"confirm":"<your tenant id>"}`, http.StatusBadRequest)
		return false
	}
	return true
}

// PurgeDataHandler handles POST /api/v1/admin/tenant/purge-data — deletes the
// caller's tenant's telemetry (ClickHouse/Quickwit/Neo4j + derived Postgres) but
// keeps the account (users/keys/plan) intact. GDPR "delete my data".
func (p *Purger) PurgeDataHandler(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantOf(r)
	if !requireConfirmation(w, r, tenantID) {
		return
	}
	log.Printf("tenantdata: PURGE DATA requested for tenant %s by %s", tenantID, r.Header.Get("X-User-Subject"))
	res := p.PurgeTenant(r.Context(), tenantID, false)
	writeResult(w, res)
}

// CloseAccountHandler handles POST /api/v1/admin/tenant/close — full offboarding:
// purges telemetry AND deletes the account (users, keys, alert rules, tenant row).
func (p *Purger) CloseAccountHandler(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantOf(r)
	if tenantID == "default" {
		http.Error(w, "the built-in 'default' tenant cannot be closed", http.StatusForbidden)
		return
	}
	if !requireConfirmation(w, r, tenantID) {
		return
	}
	log.Printf("tenantdata: CLOSE ACCOUNT requested for tenant %s by %s", tenantID, r.Header.Get("X-User-Subject"))
	res := p.PurgeTenant(r.Context(), tenantID, true)
	writeResult(w, res)
}

func writeResult(w http.ResponseWriter, res *Result) {
	w.Header().Set("Content-Type", "application/json")
	// 207-ish semantics: report partial failures in the body but still 200, since
	// each store was attempted; the caller inspects errors[].
	_ = json.NewEncoder(w).Encode(res)
}
