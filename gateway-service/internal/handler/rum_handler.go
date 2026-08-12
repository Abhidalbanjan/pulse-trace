package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
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
	// Timestamp is the client-side event time in epoch milliseconds. RUM is
	// inherently client-timed (the user's browser is the source of truth for when
	// a page view / vital / error happened), so when the SDK supplies it we honour
	// it; when absent the row falls back to the table's server-side now() default.
	Timestamp int64 `json:"timestamp,omitempty"`
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
	insertQuery.WriteString("INSERT INTO pulsetrace.rum_events (TenantID, SessionID, Type, Path, UserAgent, MetricName, MetricValue, ErrorMsg, ErrorStack, TraceID, SpanID, Timestamp) FORMAT JSONEachRow\n")

	for _, ev := range events {
		row := map[string]interface{}{
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
		}
		// Honour a client-supplied event time; otherwise omit the column so the
		// table's now() DEFAULT applies. ClickHouse DateTime64(3) parses this
		// "YYYY-MM-DD hh:mm:ss.mmm" UTC form directly via JSONEachRow.
		if ev.Timestamp > 0 {
			row["Timestamp"] = time.UnixMilli(ev.Timestamp).UTC().Format("2006-01-02 15:04:05.000")
		}
		b, _ := json.Marshal(row)
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

	// Web Vitals are reported at the 75th percentile per Google's Core Web Vitals
	// methodology (a p75 LCP is what a "good/needs-improvement/poor" rating keys
	// off), so p75 is the number that actually matters — avg understates the tail
	// real users feel. We surface both: p75_value for the rating, avg_value for
	// the trend line, and p95_value for the worst-case.
	query := `
		SELECT
			Type,
			MetricName,
			avg(MetricValue) as avg_value,
			quantile(0.75)(MetricValue) as p75_value,
			quantile(0.95)(MetricValue) as p95_value,
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

// rumInterval resolves the interval query param to a (SQL interval, bucket
// expression) pair, defaulting to 24h. It reuses the metric pillar's shared
// bucketing so RUM trends bucket exactly like every other time series.
func rumInterval(r *http.Request) (sqlInterval, bucketExpr string) {
	interval := r.URL.Query().Get("interval")
	if _, ok := intervalToSQL[interval]; !ok {
		interval = "24h"
	}
	return intervalToSQL[interval], metricIntervalToBucket[interval]
}

// GetTrends returns time-bucketed p75 web-vital values so the UI can render
// trend lines instead of a single point-in-time number — the difference between
// "LCP is 2.4s" and "LCP has been climbing all afternoon". Scoped to the tenant.
//
//	GET /api/v1/rum/trends?interval=24h|7d
func (h *RUMHandler) GetTrends(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	sqlInterval, bucketExpr := rumInterval(r)

	query := fmt.Sprintf(`
		SELECT
			%s as time_bucket,
			MetricName as metric,
			quantile(0.75)(MetricValue) as p75,
			count() as count
		FROM pulsetrace.rum_events
		WHERE TenantID = {tenant:String} AND Type = 'web_vitals' AND Timestamp >= now() - INTERVAL %s
		GROUP BY time_bucket, metric
		ORDER BY time_bucket ASC
		FORMAT JSON
	`, bucketExpr, sqlInterval)

	resp, err := h.tenantQuery(r, query)
	if err != nil {
		http.Error(w, "failed to query trends", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return
	}
	io.Copy(w, resp.Body)
}

// GetSessions returns recent user sessions stitched from their events: entry
// path, page-view and error counts, duration, and device. This is the "session
// story" — one row per real user visit rather than a firehose of events.
//
//	GET /api/v1/rum/sessions
func (h *RUMHandler) GetSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := `
		SELECT
			SessionID as session_id,
			min(Timestamp) as started_at,
			max(Timestamp) as last_seen,
			dateDiff('second', min(Timestamp), max(Timestamp)) as duration_seconds,
			countIf(Type = 'page_view') as page_views,
			countIf(Type = 'error') as errors,
			any(Path) as entry_path,
			any(UserAgent) as user_agent
		FROM pulsetrace.rum_events
		WHERE TenantID = {tenant:String} AND SessionID != '' AND Timestamp >= now() - INTERVAL 24 HOUR
		GROUP BY session_id
		ORDER BY last_seen DESC
		LIMIT 50
		FORMAT JSON
	`

	resp, err := h.tenantQuery(r, query)
	if err != nil {
		http.Error(w, "failed to query sessions", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return
	}

	// Enrich each session with a parsed device/browser label server-side, so the
	// UA-parsing logic lives in exactly one place (and is unit-tested).
	var chResult struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chResult); err != nil {
		http.Error(w, "failed to decode sessions", http.StatusInternalServerError)
		return
	}
	for _, row := range chResult.Data {
		browser, os, device := classifyUserAgent(toStr(row["user_agent"]))
		row["browser"] = browser
		row["os"] = os
		row["device"] = device
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": chResult.Data})
}

// cwvVerdict rates a Core Web Vital's p75 value against Google's
// good/needs-improvement/poor thresholds. Pure and unit-tested so the rating
// logic has one source of truth. Latency metrics are in milliseconds; CLS is
// unitless. An unknown metric returns "unknown" rather than a misleading rating.
func cwvVerdict(metric string, p75 float64) string {
	switch strings.ToUpper(strings.TrimSpace(metric)) {
	case "LCP":
		return cwvBand(p75, 2500, 4000)
	case "INP":
		return cwvBand(p75, 200, 500)
	case "FID":
		return cwvBand(p75, 100, 300)
	case "CLS":
		return cwvBand(p75, 0.1, 0.25)
	case "FCP":
		return cwvBand(p75, 1800, 3000)
	case "TTFB":
		return cwvBand(p75, 800, 1800)
	default:
		return "unknown"
	}
}

func cwvBand(v, good, poor float64) string {
	switch {
	case v <= good:
		return "good"
	case v <= poor:
		return "needs-improvement"
	default:
		return "poor"
	}
}

// webVitalsGroupExpr returns the ClickHouse GROUP-BY expression for a CWV
// breakdown dimension. "device" classifies the User-Agent in SQL (so p75s stay
// correct per group), mirroring classifyUserAgent's iPad→Tablet / mobile→Mobile
// rules; "page" groups by Path. The expression references only fixed column
// names — no user input — so it is injection-safe. Any other dimension defaults
// to page.
func webVitalsGroupExpr(dimension string) (expr string, resolvedDimension string) {
	if strings.EqualFold(dimension, "device") {
		ua := "UserAgent"
		return "multiIf(" +
			"positionCaseInsensitive(" + ua + ", 'iPad') > 0 OR positionCaseInsensitive(" + ua + ", 'Tablet') > 0, 'Tablet', " +
			"positionCaseInsensitive(" + ua + ", 'Mobi') > 0 OR positionCaseInsensitive(" + ua + ", 'Android') > 0 OR positionCaseInsensitive(" + ua + ", 'iPhone') > 0, 'Mobile', " +
			"'Desktop')", "device"
	}
	return "Path", "page"
}

// buildWebVitalsSQL builds the grouped p75-per-metric CWV query. Pure and
// injection-safe: the group expression is from a fixed enum and the interval is
// the validated enum from rumInterval; the tenant is bound by tenantQuery.
func buildWebVitalsSQL(dimension, sqlInterval string) (sql, resolvedDimension string) {
	groupExpr, dim := webVitalsGroupExpr(dimension)
	sql = fmt.Sprintf(`
		SELECT
			%s AS group_value,
			MetricName AS metric,
			quantile(0.75)(MetricValue) AS p75,
			count() AS samples
		FROM pulsetrace.rum_events
		WHERE TenantID = {tenant:String} AND Type = 'web_vitals' AND Timestamp >= now() - INTERVAL %s
		GROUP BY group_value, metric
		HAVING group_value != ''
		ORDER BY group_value ASC, metric ASC
		LIMIT 500
		FORMAT JSON
	`, groupExpr, sqlInterval)
	return sql, dim
}

// GetWebVitals returns Core Web Vitals (p75 per metric) broken down by page or
// device, each annotated with its good/needs-improvement/poor rating (RUM · E4).
//
//	GET /api/v1/rum/web-vitals?dimension=page|device&interval=24h|7d
func (h *RUMHandler) GetWebVitals(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	sqlInterval, _ := rumInterval(r)

	sql, dimension := buildWebVitalsSQL(r.URL.Query().Get("dimension"), sqlInterval)
	resp, err := h.tenantQuery(r, sql)
	if err != nil {
		http.Error(w, "failed to query web vitals", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"dimension": dimension, "data": []interface{}{}})
		return
	}

	var chResult struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chResult); err != nil {
		http.Error(w, "failed to decode web vitals", http.StatusInternalServerError)
		return
	}
	// Annotate each row with its rating server-side so the threshold logic is
	// unit-tested and consistent everywhere.
	for _, row := range chResult.Data {
		row["rating"] = cwvVerdict(toStr(row["metric"]), toFloat(row["p75"]))
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"dimension": dimension, "data": chResult.Data})
}

// GetDevices returns the device / browser / OS breakdown over the window. The
// User-Agent classification runs server-side (classifyUserAgent) so the same
// rules produce the sessions' device labels and this breakdown. Scoped to tenant.
//
//	GET /api/v1/rum/devices?interval=24h|7d
func (h *RUMHandler) GetDevices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	sqlInterval, _ := rumInterval(r)

	query := fmt.Sprintf(`
		SELECT UserAgent as user_agent, count() as count
		FROM pulsetrace.rum_events
		WHERE TenantID = {tenant:String} AND Timestamp >= now() - INTERVAL %s
		GROUP BY user_agent
		FORMAT JSON
	`, sqlInterval)

	resp, err := h.tenantQuery(r, query)
	if err != nil {
		http.Error(w, "failed to query devices", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"browsers":[],"os":[],"devices":[]}`)
		return
	}

	var chResult struct {
		Data []struct {
			UserAgent string `json:"user_agent"`
			Count     string `json:"count"` // ClickHouse renders UInt64 as a string in FORMAT JSON
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chResult); err != nil {
		http.Error(w, "failed to decode devices", http.StatusInternalServerError)
		return
	}

	browsers, oses, devices := map[string]int64{}, map[string]int64{}, map[string]int64{}
	for _, row := range chResult.Data {
		n, _ := strconv.ParseInt(row.Count, 10, 64)
		b, o, d := classifyUserAgent(row.UserAgent)
		browsers[b] += n
		oses[o] += n
		devices[d] += n
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"browsers": sortedBreakdown(browsers),
		"os":       sortedBreakdown(oses),
		"devices":  sortedBreakdown(devices),
	})
}

// breakdownEntry is one row of a categorical breakdown (e.g. {"Chrome", 128}).
type breakdownEntry struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// sortedBreakdown turns a category→count map into a slice ordered by count
// descending (ties broken by name, so the output is deterministic for tests).
func sortedBreakdown(m map[string]int64) []breakdownEntry {
	out := make([]breakdownEntry, 0, len(m))
	for name, count := range m {
		out = append(out, breakdownEntry{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// classifyUserAgent derives a coarse (browser, os, deviceType) triple from a raw
// User-Agent string. It intentionally covers the mainstream browsers/OSes that
// dominate real traffic and buckets everything else as "Other" rather than
// pretending to a full UA-database's precision — an honest, dependency-free
// classification good enough to answer "is our tail latency a mobile-Safari
// problem?". Order matters: Edge/Chrome both contain "Safari"; iPadOS reports as
// desktop Safari, etc., so the more specific token is checked first.
func classifyUserAgent(ua string) (browser, os, device string) {
	if ua == "" {
		return "Other", "Other", "Other"
	}

	// Browser — check the more specific tokens before the ones they embed.
	switch {
	case strings.Contains(ua, "Edg/") || strings.Contains(ua, "Edge/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/") || strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	default:
		browser = "Other"
	}

	// OS.
	switch {
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad") || strings.Contains(ua, "iPod"):
		os = "iOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	default:
		os = "Other"
	}

	// Device type.
	switch {
	case strings.Contains(ua, "iPad") || strings.Contains(ua, "Tablet"):
		device = "Tablet"
	case strings.Contains(ua, "Mobi") || strings.Contains(ua, "iPhone") || strings.Contains(ua, "Android"):
		device = "Mobile"
	default:
		device = "Desktop"
	}
	return browser, os, device
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
