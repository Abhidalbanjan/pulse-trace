package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/pulsetrace/shared/models"
)

// LogPublisher publishes log entries onto a topic. The synthetics worker uses it
// to emit an ERROR log for a failing check, which flows through the existing
// logs→alert→correlation→notification pipeline — i.e. a failed check pages
// on-call exactly the way an application ERROR does, with no parallel alert path.
type LogPublisher interface {
	PublishBatch(ctx context.Context, topic string, entries []*models.LogEntry) error
}

type SyntheticsHandler struct {
	ClickHouseURL string
	DB            *sql.DB

	// alerts is optional; when nil the worker still records results but does not
	// page (backward-compatible with the pre-alerting behaviour).
	alerts LogPublisher
	// lastFailed is edge-trigger state (per check key) so a check that stays down
	// pages once on the healthy→failing transition instead of every poll cycle.
	// Only the single worker goroutine touches it, so it needs no lock.
	lastFailed map[string]bool
}

// WithAlertPublisher wires the failure→alert path. Call before StartWorker.
func (h *SyntheticsHandler) WithAlertPublisher(p LogPublisher) *SyntheticsHandler {
	h.alerts = p
	return h
}

type SyntheticResult struct {
	Timestamp     time.Time `json:"timestamp"`
	TenantID      string    `json:"tenant_id"`
	CheckName     string    `json:"check_name"`
	URL           string    `json:"url"`
	StatusCode    int       `json:"status_code"`
	LatencyMs     float64   `json:"latency_ms"`
	Success       bool      `json:"success"`
	FailureReason string    `json:"failure_reason"`
}

// Assertion is the pass/fail contract for a single step's response. A zero value
// means "only require a 2xx status" — the historical behaviour.
type Assertion struct {
	Status       int    `json:"status,omitempty"`         // exact status code required; 0 → any 2xx
	MaxLatencyMs int    `json:"max_latency_ms,omitempty"` // latency SLA in ms; 0 → no bound
	BodyContains string `json:"body_contains,omitempty"`  // substring the body must contain; "" → no check
}

// CheckStep is one HTTP request in a (possibly multi-step) synthetic check.
type CheckStep struct {
	Name   string    `json:"name,omitempty"`
	Method string    `json:"method,omitempty"` // default GET
	URL    string    `json:"url"`
	Assert Assertion `json:"assert"`
}

// CheckSpec is the persisted multi-step definition (JSONB `spec` column). A nil
// spec denotes a legacy single-URL target, handled by falling back to one GET.
type CheckSpec struct {
	Steps []CheckStep `json:"steps"`
}

// evaluateAssertion reports whether a step's response satisfied its assertion,
// and if not, a human-readable reason. It is pure (no I/O) so the pass/fail
// contract — the heart of "richer assertions" — is unit-tested exhaustively.
func evaluateAssertion(statusCode int, latencyMs float64, body string, a Assertion) (bool, string) {
	if statusCode == 0 {
		return false, "request failed (no response)"
	}
	if a.Status != 0 {
		if statusCode != a.Status {
			return false, fmt.Sprintf("expected status %d, got %d", a.Status, statusCode)
		}
	} else if statusCode < 200 || statusCode >= 300 {
		return false, fmt.Sprintf("expected a 2xx status, got %d", statusCode)
	}
	if a.MaxLatencyMs > 0 && latencyMs > float64(a.MaxLatencyMs) {
		return false, fmt.Sprintf("latency %.0fms exceeded %dms SLA", latencyMs, a.MaxLatencyMs)
	}
	if a.BodyContains != "" && !strings.Contains(body, a.BodyContains) {
		return false, fmt.Sprintf("body did not contain %q", a.BodyContains)
	}
	return true, ""
}

func NewSyntheticsHandler(clickhouseURL string, db *sql.DB) *SyntheticsHandler {
	handler := &SyntheticsHandler{
		ClickHouseURL: clickhouseURL,
		DB:            db,
		lastFailed:    make(map[string]bool),
	}
	handler.initClickHouseTable()
	handler.initPostgresTable()
	return handler
}

func (h *SyntheticsHandler) initPostgresTable() {
	if h.DB == nil {
		log.Println("[SyntheticsHandler] WARNING: Postgres DB is nil, targets will not be persistent.")
		return
	}
	query := `
		CREATE TABLE IF NOT EXISTS synthetic_targets (
			id SERIAL PRIMARY KEY,
			tenant_id VARCHAR(50) NOT NULL DEFAULT 'default',
			url VARCHAR(255) NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(tenant_id, url)
		);
	`
	_, err := h.DB.Exec(query)
	if err != nil {
		log.Printf("[SyntheticsHandler] WARNING: Failed to create Postgres synthetic_targets table: %v", err)
	} else {
		log.Println("[SyntheticsHandler] Postgres synthetic_targets table initialized.")
	}
	// Backfill tenant_id on a pre-existing single-tenant table (best-effort), and
	// add the multi-step columns (name + JSONB spec) for richer checks. Legacy
	// rows keep a NULL spec and run as a single GET on their url.
	_, _ = h.DB.Exec("ALTER TABLE synthetic_targets ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(50) NOT NULL DEFAULT 'default'")
	_, _ = h.DB.Exec("ALTER TABLE synthetic_targets ADD COLUMN IF NOT EXISTS name VARCHAR(120) NOT NULL DEFAULT ''")
	_, _ = h.DB.Exec("ALTER TABLE synthetic_targets ADD COLUMN IF NOT EXISTS spec JSONB")

	// Repair the uniqueness constraint on deployments that predate multi-tenancy.
	//
	// Those tables were created with a single-column UNIQUE(url). CREATE TABLE IF
	// NOT EXISTS above is a no-op against them, and the ALTERs add tenant_id as a
	// column but not to the constraint — so CreateTarget's
	// `ON CONFLICT (tenant_id, url)` had no matching constraint and every check
	// creation failed with 42P10 (`no unique or exclusion constraint matching the
	// ON CONFLICT specification`). Fresh installs were fine, which is why CI never
	// saw it: only upgraded databases are affected.
	//
	// UNIQUE(url) is also wrong on its own terms once tenants exist — it stops two
	// tenants registering the same URL, and turns a collision into a signal that
	// someone else already monitors it.
	if _, err := h.DB.Exec("ALTER TABLE synthetic_targets DROP CONSTRAINT IF EXISTS synthetic_targets_url_key"); err != nil {
		log.Printf("[SyntheticsHandler] WARNING: could not drop legacy UNIQUE(url): %v", err)
	}
	if _, err := h.DB.Exec(`DO $$ BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM pg_constraint WHERE conname = 'synthetic_targets_tenant_id_url_key'
		) THEN
			ALTER TABLE synthetic_targets
				ADD CONSTRAINT synthetic_targets_tenant_id_url_key UNIQUE (tenant_id, url);
		END IF;
	END $$;`); err != nil {
		log.Printf("[SyntheticsHandler] WARNING: could not add UNIQUE(tenant_id, url): %v", err)
	}
}

func (h *SyntheticsHandler) initClickHouseTable() {
	query := `
		CREATE TABLE IF NOT EXISTS pulsetrace.synthetic_results (
			Timestamp DateTime64(3) DEFAULT now(),
			TenantID String DEFAULT 'default',
			URL String,
			StatusCode Int32,
			LatencyMs Float64,
			Success UInt8
		) ENGINE = MergeTree()
		PARTITION BY TenantID
		ORDER BY (Timestamp, URL)
		TTL toDateTime(Timestamp) + INTERVAL 7 DAY;
	`

	req, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[SyntheticsHandler] WARNING: Failed to create ClickHouse table: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[SyntheticsHandler] WARNING: ClickHouse table creation returned %d: %s", resp.StatusCode, string(body))
	} else {
		log.Println("[SyntheticsHandler] ClickHouse synthetic_results table initialized.")
	}

	// Add columns to a pre-existing table (best-effort). TenantID backfills old
	// rows to 'default'; CheckName/FailureReason carry the multi-step + assertion
	// context so the results view can show which check failed and why.
	for _, alterSQL := range []string{
		"ALTER TABLE pulsetrace.synthetic_results ADD COLUMN IF NOT EXISTS TenantID String DEFAULT 'default'",
		"ALTER TABLE pulsetrace.synthetic_results ADD COLUMN IF NOT EXISTS CheckName String DEFAULT ''",
		"ALTER TABLE pulsetrace.synthetic_results ADD COLUMN IF NOT EXISTS FailureReason String DEFAULT ''",
	} {
		alter, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(alterSQL))
		alter.SetBasicAuth(clickhouseUser, clickhousePassword)
		if aresp, aerr := client.Do(alter); aerr == nil {
			aresp.Body.Close()
		}
	}
}

// syntheticTarget is a monitored check loaded from Postgres: its owning tenant,
// display name, legacy url, and (for multi-step checks) a parsed step list.
type syntheticTarget struct {
	tenantID string
	name     string
	url      string
	steps    []CheckStep
}

// maxProbeBodyBytes caps how much of a response body we read for a
// body_contains assertion, so a huge/streaming response can't exhaust memory.
const maxProbeBodyBytes = 64 * 1024

// StartWorker begins polling the endpoints in the background
func (h *SyntheticsHandler) StartWorker() {
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			if h.DB == nil {
				continue // Need DB for targets
			}

			targets, err := h.loadTargets()
			if err != nil {
				log.Printf("[SyntheticsWorker] Failed to query targets: %v", err)
				continue
			}
			if len(targets) == 0 {
				continue
			}

			var results []SyntheticResult
			for _, tgt := range targets {
				results = append(results, h.runCheck(client, tgt)...)
			}
			h.flushResults(results)
		}
	}()
}

// loadTargets reads every configured check, parsing the JSONB spec into steps.
// A NULL/empty spec is a legacy single-URL target and yields no steps (runCheck
// falls back to one GET on url).
func (h *SyntheticsHandler) loadTargets() ([]syntheticTarget, error) {
	rows, err := h.DB.Query("SELECT tenant_id, COALESCE(name,''), url, spec FROM synthetic_targets")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []syntheticTarget
	for rows.Next() {
		var tgt syntheticTarget
		var specJSON sql.NullString
		if err := rows.Scan(&tgt.tenantID, &tgt.name, &tgt.url, &specJSON); err != nil {
			continue
		}
		if specJSON.Valid && strings.TrimSpace(specJSON.String) != "" {
			var spec CheckSpec
			if err := json.Unmarshal([]byte(specJSON.String), &spec); err == nil {
				tgt.steps = spec.Steps
			}
		}
		targets = append(targets, tgt)
	}
	return targets, nil
}

// runCheck executes one check's steps in order, records a result per step, and
// pages (once, edge-triggered) when the check transitions healthy→failing. A
// multi-step check stops at its first failing step — later steps typically
// depend on earlier ones (login → add-to-cart → checkout), so continuing past a
// failure would report misleading downstream errors.
func (h *SyntheticsHandler) runCheck(client *http.Client, tgt syntheticTarget) []SyntheticResult {
	steps := tgt.steps
	if len(steps) == 0 {
		// Legacy / simple target: a single GET expecting any 2xx.
		steps = []CheckStep{{Name: tgt.name, Method: http.MethodGet, URL: tgt.url}}
	}

	name := tgt.name
	if name == "" {
		name = tgt.url
	}

	now := time.Now()
	var results []SyntheticResult
	var failure string

	for _, step := range steps {
		// Belt-and-suspenders SSRF check: a row could predate validateProbeURL or
		// have been inserted out of band.
		if err := validateProbeURL(step.URL); err != nil {
			failure = fmt.Sprintf("step %q disallowed: %v", stepLabel(step), err)
			results = append(results, SyntheticResult{Timestamp: now, TenantID: tgt.tenantID, CheckName: name, URL: step.URL, StatusCode: 0, Success: false, FailureReason: failure})
			break
		}

		status, latency, body := h.probe(client, step)
		ok, reason := evaluateAssertion(status, latency, body, step.Assert)
		results = append(results, SyntheticResult{
			Timestamp: now, TenantID: tgt.tenantID, CheckName: name, URL: step.URL,
			StatusCode: status, LatencyMs: latency, Success: ok, FailureReason: reason,
		})
		if !ok {
			failure = fmt.Sprintf("step %q: %s", stepLabel(step), reason)
			break
		}
	}

	h.pageOnTransition(tgt.tenantID, name, failure)
	return results
}

// probe issues one step's request and returns its status, latency (ms), and a
// bounded slice of the response body (only read when a body assertion needs it).
func (h *SyntheticsHandler) probe(client *http.Client, step CheckStep) (status int, latencyMs float64, body string) {
	method := strings.ToUpper(strings.TrimSpace(step.Method))
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequest(method, step.URL, nil)
	if err != nil {
		return 0, 0, ""
	}
	start := time.Now()
	resp, err := client.Do(req)
	latencyMs = float64(time.Since(start).Milliseconds())
	if err != nil {
		return 0, latencyMs, ""
	}
	defer resp.Body.Close()
	if step.Assert.BodyContains != "" {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBodyBytes))
		body = string(b)
	} else {
		// Drain a little so the connection can be reused, without buffering it all.
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBodyBytes))
	}
	return resp.StatusCode, latencyMs, body
}

// pageOnTransition emits an ERROR log (→ alert → incident → notification) only
// when a check flips from healthy to failing, so a persistently-down check pages
// once rather than every 10s poll. A recovery clears the remembered state.
func (h *SyntheticsHandler) pageOnTransition(tenantID, checkName, failure string) {
	key := tenantID + "\x00" + checkName
	if failure == "" {
		delete(h.lastFailed, key) // recovered (or healthy) — reset the edge
		return
	}
	if h.lastFailed[key] {
		return // already paged for this outage
	}
	h.lastFailed[key] = true

	if h.alerts == nil {
		return
	}
	entry := &models.LogEntry{
		ID:          uuid.NewString(),
		TenantID:    tenantID,
		ServiceName: "synthetic:" + checkName,
		Level:       models.LogLevelError,
		Message:     fmt.Sprintf("Synthetic check %q failed: %s", checkName, failure),
		Timestamp:   time.Now().UTC(),
		CreatedAt:   time.Now().UTC(),
	}
	if err := h.alerts.PublishBatch(context.Background(), "logs", []*models.LogEntry{entry}); err != nil {
		log.Printf("[SyntheticsWorker] failed to publish failure alert for %q: %v", checkName, err)
	}
}

// stepLabel is the step's name, falling back to its URL.
func stepLabel(s CheckStep) string {
	if s.Name != "" {
		return s.Name
	}
	return s.URL
}

func (h *SyntheticsHandler) flushResults(results []SyntheticResult) {
	if len(results) == 0 {
		return
	}

	var insertQuery bytes.Buffer
	insertQuery.WriteString("INSERT INTO pulsetrace.synthetic_results (Timestamp, TenantID, CheckName, URL, StatusCode, LatencyMs, Success, FailureReason) FORMAT JSONEachRow\n")

	for _, res := range results {
		// Convert time to string for CH JSONEachRow
		type chResult struct {
			Timestamp     string  `json:"Timestamp"`
			TenantID      string  `json:"TenantID"`
			CheckName     string  `json:"CheckName"`
			URL           string  `json:"URL"`
			StatusCode    int     `json:"StatusCode"`
			LatencyMs     float64 `json:"LatencyMs"`
			Success       uint8   `json:"Success"`
			FailureReason string  `json:"FailureReason"`
		}

		succ := uint8(0)
		if res.Success {
			succ = 1
		}

		tenantID := res.TenantID
		if tenantID == "" {
			tenantID = "default"
		}

		ch := chResult{
			Timestamp:     res.Timestamp.Format("2006-01-02 15:04:05.000"),
			TenantID:      tenantID,
			CheckName:     res.CheckName,
			URL:           res.URL,
			StatusCode:    res.StatusCode,
			LatencyMs:     res.LatencyMs,
			Success:       succ,
			FailureReason: res.FailureReason,
		}

		b, _ := json.Marshal(ch)
		insertQuery.Write(b)
		insertQuery.WriteString("\n")
	}

	req, _ := http.NewRequest("POST", h.ClickHouseURL, &insertQuery)
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[SyntheticsWorker] Insert failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[SyntheticsWorker] Insert returned %d: %s", resp.StatusCode, string(body))
	}
}

// GetResults serves analytics to the frontend
func (h *SyntheticsHandler) GetResults(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	// Get the last 60 minutes of data for sparklines, plus aggregated stats,
	// scoped to the caller's tenant.
	query := `
		SELECT
			URL,
			any(CheckName) as check_name,
			avg(LatencyMs) as avg_latency_ms,
			avg(Success) * 100 as uptime_percent,
			groupArray(LatencyMs) as latency_history,
			argMax(FailureReason, Timestamp) as last_failure
		FROM pulsetrace.synthetic_results
		WHERE TenantID = {tenant:String} AND Timestamp >= now() - INTERVAL 1 HOUR
		GROUP BY URL
		FORMAT JSON
	`

	reqURL := h.ClickHouseURL + "?param_tenant=" + url.QueryEscape(tenantID)
	req, _ := http.NewRequest("POST", reqURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to query synthetics", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return
	}
	io.Copy(w, resp.Body)
}

// validateProbeURL guards against SSRF: the synthetics worker runs inside the
// cluster and will issue a GET to whatever URL a tenant registers, so an
// unvalidated target could be pointed at the cloud metadata endpoint
// (169.254.169.254), localhost, or a private-range internal service. This
// rejects anything that isn't a plain http(s) URL to a public host.
//
// It's pure (no DNS) so it's unit-testable and can't be defeated by a slow
// resolver; the handler layers a best-effort DNS resolution check on top for
// hostnames that resolve into private ranges. Literal-IP targets — the direct
// attack — are fully blocked here.
func validateProbeURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http and https URLs may be probed")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL has no host")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("localhost is not a permitted probe target")
	}
	// If the host is a literal IP, classify it directly.
	if ip := net.ParseIP(host); ip != nil && !isPublicIP(ip) {
		return fmt.Errorf("private, loopback, or link-local addresses may not be probed")
	}
	return nil
}

// isPublicIP reports whether ip is a globally routable unicast address — i.e.
// not loopback, private (RFC1918 / ULA), link-local (incl. 169.254.0.0/16
// cloud metadata), or unspecified.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	return true
}

// resolvesToPrivate is the handler-side DNS check: a hostname that resolves
// entirely (or partly) into non-public space is refused too, catching
// internal.corp-style names pointing at private IPs. Best-effort — a resolution
// failure is treated as "can't confirm private" and left to the literal checks.
func resolvesToPrivate(host string) bool {
	if net.ParseIP(host) != nil {
		return false // literal IPs are handled by validateProbeURL
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return true
		}
	}
	return false
}

// CreateTarget registers a synthetic check. It accepts either the legacy shape
// (`{"url": "..."}` — a single GET expecting 2xx) or a multi-step check
// (`{"name": "...", "steps": [{method,url,assert},...]}`). Both persist into
// synthetic_targets; a multi-step check stores its JSONB spec and uses the first
// step's URL as the row's url (satisfying the UNIQUE(tenant,url) key and keeping
// delete-by-url working).
func (h *SyntheticsHandler) CreateTarget(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		URL   string      `json:"url"`
		Name  string      `json:"name"`
		Steps []CheckStep `json:"steps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Normalise into steps. Legacy single-url payloads become a one-step check.
	steps := payload.Steps
	if len(steps) == 0 {
		if payload.URL == "" {
			http.Error(w, "provide either a url or a non-empty steps array", http.StatusBadRequest)
			return
		}
		steps = []CheckStep{{Method: http.MethodGet, URL: payload.URL}}
	}

	// Validate every step URL against SSRF before persisting.
	for i := range steps {
		steps[i].URL = strings.TrimSpace(steps[i].URL)
		if err := validateProbeURL(steps[i].URL); err != nil {
			http.Error(w, fmt.Sprintf("step %d: %v", i+1, err), http.StatusBadRequest)
			return
		}
		if u, _ := url.Parse(steps[i].URL); u != nil && resolvesToPrivate(u.Hostname()) {
			http.Error(w, fmt.Sprintf("step %d host resolves to a private or loopback address", i+1), http.StatusBadRequest)
			return
		}
	}

	if h.DB == nil {
		http.Error(w, "database unavailable", http.StatusInternalServerError)
		return
	}

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	name := strings.TrimSpace(payload.Name)
	rowURL := steps[0].URL

	// A single default GET step is stored spec-less (legacy row); anything richer
	// (multiple steps or any assertion) persists its spec.
	var specJSON interface{}
	if len(steps) > 1 || steps[0].Assert != (Assertion{}) || steps[0].Method != http.MethodGet || name != "" {
		b, err := json.Marshal(CheckSpec{Steps: steps})
		if err != nil {
			http.Error(w, "failed to encode check spec", http.StatusInternalServerError)
			return
		}
		specJSON = string(b)
	}

	_, err := h.DB.Exec(
		`INSERT INTO synthetic_targets (tenant_id, url, name, spec) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, url) DO UPDATE SET name = EXCLUDED.name, spec = EXCLUDED.spec`,
		tenantID, rowURL, name, specJSON,
	)
	if err != nil {
		log.Printf("[SyntheticsHandler] Failed to insert target: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	io.WriteString(w, `{"status":"ok"}`)
}

// ListTargets returns the configured checks for the caller's tenant so the UI
// can render the check list and step editor. Scoped to the tenant.
//
//	GET /api/v1/synthetics/tests
func (h *SyntheticsHandler) ListTargets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.DB == nil {
		io.WriteString(w, `{"data": []}`)
		return
	}
	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	rows, err := h.DB.Query("SELECT COALESCE(name,''), url, spec FROM synthetic_targets WHERE tenant_id = $1 ORDER BY url", tenantID)
	if err != nil {
		http.Error(w, "failed to list checks", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type checkOut struct {
		Name  string      `json:"name"`
		URL   string      `json:"url"`
		Steps []CheckStep `json:"steps"`
	}
	out := []checkOut{}
	for rows.Next() {
		var c checkOut
		var specJSON sql.NullString
		if err := rows.Scan(&c.Name, &c.URL, &specJSON); err != nil {
			continue
		}
		if specJSON.Valid && strings.TrimSpace(specJSON.String) != "" {
			var spec CheckSpec
			if err := json.Unmarshal([]byte(specJSON.String), &spec); err == nil {
				c.Steps = spec.Steps
			}
		}
		if len(c.Steps) == 0 {
			c.Steps = []CheckStep{{Method: http.MethodGet, URL: c.URL}}
		}
		out = append(out, c)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}

// DeleteTarget removes a synthetic endpoint for the caller's tenant.
//
//	DELETE /api/v1/synthetics/tests?url=<url>
func (h *SyntheticsHandler) DeleteTarget(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if target == "" {
		http.Error(w, "url query parameter is required", http.StatusBadRequest)
		return
	}
	if h.DB == nil {
		http.Error(w, "database unavailable", http.StatusInternalServerError)
		return
	}

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	res, err := h.DB.Exec("DELETE FROM synthetic_targets WHERE tenant_id = $1 AND url = $2", tenantID, target)
	if err != nil {
		log.Printf("[SyntheticsHandler] Failed to delete target: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"status":"deleted"}`)
}
