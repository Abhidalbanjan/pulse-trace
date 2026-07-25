package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/pulsetrace/shared/metering"
)

type RUMHandler struct {
	ClickHouseURL string
	meter         *metering.Meter
}

type RUMEvent struct {
	SessionID   string  `json:"session_id"`
	Type        string  `json:"type"` // "web_vitals" or "error" or "page_view"
	Path        string  `json:"path"`
	UserAgent   string  `json:"user_agent"`
	MetricName  string  `json:"metric_name,omitempty"` // LCP, FID, CLS
	MetricValue float64 `json:"metric_value,omitempty"`
	ErrorMsg    string  `json:"error_msg,omitempty"`
	ErrorStack  string  `json:"error_stack,omitempty"`
	TraceID     string  `json:"trace_id,omitempty"` // W3C trace id shared with backend API calls made during this page view
	SpanID      string  `json:"span_id,omitempty"`
}

func NewRUMHandler(clickhouseURL string, meter *metering.Meter) *RUMHandler {
	handler := &RUMHandler{ClickHouseURL: clickhouseURL, meter: meter}
	handler.initTable()
	return handler
}

func (h *RUMHandler) initTable() {
	query := `
		CREATE TABLE IF NOT EXISTS pulsetrace.rum_events (
			Timestamp DateTime64(3) DEFAULT now(),
			TenantID String DEFAULT 'default',
			SessionID String,
			Type String,
			Path String,
			UserAgent String,
			MetricName String,
			MetricValue Float64,
			ErrorMsg String,
			ErrorStack String,
			TraceID String DEFAULT '',
			SpanID String DEFAULT ''
		) ENGINE = MergeTree()
		PARTITION BY TenantID
		ORDER BY (Timestamp, Type)
		TTL toDateTime(Timestamp) + INTERVAL 7 DAY;
	`

	req, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[RUMHandler] WARNING: Failed to create ClickHouse table: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[RUMHandler] WARNING: ClickHouse table creation returned %d: %s", resp.StatusCode, string(body))
	} else {
		log.Println("[RUMHandler] ClickHouse rum_events table initialized.")
	}

	// Table may already exist from before these columns were added - add them if
	// missing. TenantID backfills existing rows with 'default' (the pre-multi-tenant
	// value), so old data stays visible to the default tenant rather than vanishing.
	alterQuery := `
		ALTER TABLE pulsetrace.rum_events
			ADD COLUMN IF NOT EXISTS TenantID String DEFAULT 'default',
			ADD COLUMN IF NOT EXISTS TraceID String DEFAULT '',
			ADD COLUMN IF NOT EXISTS SpanID String DEFAULT ''
	`
	alterReq, _ := http.NewRequest("POST", h.ClickHouseURL, bytes.NewBufferString(alterQuery))
	alterReq.SetBasicAuth(clickhouseUser, clickhousePassword)
	alterResp, err := client.Do(alterReq)
	if err != nil {
		log.Printf("[RUMHandler] WARNING: Failed to alter rum_events table: %v", err)
		return
	}
	defer alterResp.Body.Close()
	if alterResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(alterResp.Body)
		log.Printf("[RUMHandler] WARNING: ClickHouse table alter returned %d: %s", alterResp.StatusCode, string(body))
	}
}

// Ingest handles POST requests from the browser
func (h *RUMHandler) Ingest(w http.ResponseWriter, r *http.Request) {
	var events []RUMEvent
	if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if len(events) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Tenant is resolved server-side from the gateway-verified header, never from
	// the browser payload — a RUM event can't choose its own tenant.
	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	// Batch insert into ClickHouse. Keys are written in the table's PascalCase
	// column names so JSONEachRow maps them correctly (and the tenant is stamped
	// onto every row).
	var insertQuery bytes.Buffer
	insertQuery.WriteString("INSERT INTO pulsetrace.rum_events (TenantID, SessionID, Type, Path, UserAgent, MetricName, MetricValue, ErrorMsg, ErrorStack, TraceID, SpanID) FORMAT JSONEachRow\n")

	for _, ev := range events {
		b, _ := json.Marshal(map[string]interface{}{
			"TenantID":    tenantID,
			"SessionID":   ev.SessionID,
			"Type":        ev.Type,
			"Path":        ev.Path,
			"UserAgent":   ev.UserAgent,
			"MetricName":  ev.MetricName,
			"MetricValue": ev.MetricValue,
			"ErrorMsg":    ev.ErrorMsg,
			"ErrorStack":  ev.ErrorStack,
			"TraceID":     ev.TraceID,
			"SpanID":      ev.SpanID,
		})
		insertQuery.Write(b)
		insertQuery.WriteString("\n")
	}

	req, _ := http.NewRequest("POST", h.ClickHouseURL, &insertQuery)
	req.SetBasicAuth(clickhouseUser, clickhousePassword)
	
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[RUMHandler] Failed to insert RUM events: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[RUMHandler] Insert returned %d: %s", resp.StatusCode, string(body))
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Meter the accepted RUM events against the tenant's usage.
	h.meter.Record(r.Context(), tenantID, metering.SignalRUM, int64(len(events)))

	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"status": "ok"}`)
}

// tenantQuery POSTs a ClickHouse query scoped to one tenant, passing the tenant
// as a bind parameter (param_tenant → {tenant:String}) so it's never
// string-concatenated into SQL.
func (h *RUMHandler) tenantQuery(r *http.Request, query string) (*http.Response, error) {
	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}
	reqURL := h.ClickHouseURL + "?param_tenant=" + url.QueryEscape(tenantID)
	req, _ := http.NewRequest("POST", reqURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)
	return (&http.Client{Timeout: 5 * time.Second}).Do(req)
}

// GetAnalytics queries RUM data for the frontend dashboard, scoped to the caller's tenant.
func (h *RUMHandler) GetAnalytics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := `
		SELECT
			Type,
			MetricName,
			avg(MetricValue) as avg_value,
			count() as count
		FROM pulsetrace.rum_events
		WHERE TenantID = {tenant:String} AND Timestamp >= now() - INTERVAL 24 HOUR
		GROUP BY Type, MetricName
		FORMAT JSON
	`

	resp, err := h.tenantQuery(r, query)
	if err != nil {
		http.Error(w, "failed to query analytics", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return
	}
	io.Copy(w, resp.Body)
}

// GetErrors queries recent frontend errors, scoped to the caller's tenant.
func (h *RUMHandler) GetErrors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := `
		SELECT
			Timestamp as timestamp,
			Path as path,
			ErrorMsg as error_msg,
			ErrorStack as error_stack,
			UserAgent as user_agent,
			TraceID as trace_id
		FROM pulsetrace.rum_events
		WHERE TenantID = {tenant:String} AND Type = 'error'
		ORDER BY Timestamp DESC
		LIMIT 50
		FORMAT JSON
	`

	resp, err := h.tenantQuery(r, query)
	if err != nil {
		http.Error(w, "failed to query errors", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return
	}
	io.Copy(w, resp.Body)
}
