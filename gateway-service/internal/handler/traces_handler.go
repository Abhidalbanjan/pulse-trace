package handler

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// TracesHandler powers first-class trace search + retrieval over ClickHouse
// otel_traces (Traces · E1). Search groups spans into per-trace summaries with
// APM-style filters (service, operation, duration, status, span tags); retrieval
// returns every span of one trace for the waterfall + span-detail panel.
//
// All user-supplied values are passed as ClickHouse bind parameters (never
// string-concatenated), and every read goes through clickHouseClient.queryScoped
// so tenant isolation is enforced at the choke point.
type TracesHandler struct {
	ch *clickHouseClient
}

func NewTracesHandler(clickhouseURL string) *TracesHandler {
	return &TracesHandler{ch: &clickHouseClient{URL: clickhouseURL}}
}

// defaultTraceLimit / maxTraceLimit bound the result set so a search can't ask
// ClickHouse for an unbounded scan.
const (
	defaultTraceLimit = 100
	maxTraceLimit     = 200
)

// traceTag is a single span-attribute filter (key = value).
type traceTag struct{ Key, Value string }

// traceFilters is the parsed, validated search request.
type traceFilters struct {
	Service   string
	Operation string
	Status    string // "error" | "ok" | "" (any)
	MinMs     float64
	HasMin    bool
	MaxMs     float64
	HasMax    bool
	Tags      []traceTag
	Interval  string // validated enum from resolveInterval
	Limit     int
}

// parseTagFilters turns repeated `tag=key:value` query values into structured
// filters, dropping malformed entries (missing separator / empty key). The value
// may itself contain ':' (e.g. a URL), so only the first ':' splits.
func parseTagFilters(raw []string) []traceTag {
	out := make([]traceTag, 0, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		i := strings.IndexByte(r, ':')
		if i <= 0 || i == len(r)-1 {
			continue // no separator, empty key, or empty value
		}
		out = append(out, traceTag{Key: strings.TrimSpace(r[:i]), Value: strings.TrimSpace(r[i+1:])})
	}
	return out
}

// parseTraceFilters extracts and validates the search filters from the request.
func parseTraceFilters(r *http.Request) traceFilters {
	q := r.URL.Query()
	f := traceFilters{
		Service:   strings.TrimSpace(q.Get("service")),
		Operation: strings.TrimSpace(q.Get("operation")),
		Tags:      parseTagFilters(q["tag"]),
		Limit:     defaultTraceLimit,
	}

	switch strings.ToLower(q.Get("status")) {
	case "error", "err", "errors":
		f.Status = "error"
	case "ok", "success":
		f.Status = "ok"
	}

	if v := q.Get("minDurationMs"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
			f.MinMs, f.HasMin = ms, true
		}
	}
	if v := q.Get("maxDurationMs"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
			f.MaxMs, f.HasMax = ms, true
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.Limit = n
		}
	}
	if f.Limit > maxTraceLimit {
		f.Limit = maxTraceLimit
	}

	// interval is enum-validated (default 1h) so it is safe to inline in SQL.
	_, f.Interval, _ = resolveInterval(q.Get("interval"))
	return f
}

// buildTraceSearchSQL builds the grouped trace-search query and its bind
// parameters. Pure (no I/O) so the SQL construction is unit-testable and its
// injection-safety asserted. The tenant param is added later by queryScoped.
//
// Semantics: group spans by TraceId within the interval; the "root" is the
// entry span (empty ParentSpanId). service/operation match the root; duration is
// the root span's total; a trace matches a tag filter when any of its spans
// carries that attribute value.
func buildTraceSearchSQL(f traceFilters) (string, map[string]string) {
	params := map[string]string{}

	var having []string
	if f.Service != "" {
		params["svc"] = f.Service
		having = append(having, "root_service = {svc:String}")
	}
	if f.Operation != "" {
		params["op"] = f.Operation
		having = append(having, "root_operation = {op:String}")
	}
	if f.HasMin {
		params["minms"] = strconv.FormatFloat(f.MinMs, 'f', -1, 64)
		having = append(having, "duration_ms >= {minms:Float64}")
	}
	if f.HasMax {
		params["maxms"] = strconv.FormatFloat(f.MaxMs, 'f', -1, 64)
		having = append(having, "duration_ms <= {maxms:Float64}")
	}
	switch f.Status {
	case "error":
		having = append(having, "error_count > 0")
	case "ok":
		having = append(having, "error_count = 0")
	}
	for i, tag := range f.Tags {
		kp, vp := fmt.Sprintf("tk%d", i), fmt.Sprintf("tv%d", i)
		params[kp] = tag.Key
		params[vp] = tag.Value
		having = append(having, fmt.Sprintf("countIf(SpanAttributes[{%s:String}] = {%s:String}) > 0", kp, vp))
	}

	havingClause := ""
	if len(having) > 0 {
		havingClause = "HAVING " + strings.Join(having, " AND ")
	}

	// f.Interval and f.Limit are validated (enum / positive int) so they are safe
	// to inline; every user string/number is a bind param above.
	sql := fmt.Sprintf(`
		SELECT
			TraceId AS trace_id,
			anyIf(ServiceName, ParentSpanId = '') AS root_service,
			anyIf(SpanName, ParentSpanId = '') AS root_operation,
			min(Timestamp) AS start_time,
			maxIf(Duration, ParentSpanId = '') / 1000000.0 AS duration_ms,
			count() AS span_count,
			countIf(StatusCode = 'STATUS_CODE_ERROR') AS error_count,
			if(countIf(StatusCode = 'STATUS_CODE_ERROR') > 0, 'error', 'ok') AS status
		FROM pulsetrace.otel_traces
		WHERE ResourceAttributes['tenant.id'] = {tenant:String}
		  AND Timestamp >= now() - INTERVAL %s
		GROUP BY TraceId
		%s
		ORDER BY start_time DESC
		LIMIT %d
		FORMAT JSON
	`, f.Interval, havingClause, f.Limit)

	return sql, params
}

// Search handles GET /api/v1/traces — grouped trace summaries matching filters.
func (h *TracesHandler) Search(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sql, params := buildTraceSearchSQL(parseTraceFilters(r))
	resp, err := h.ch.queryScoped(tenantFromRequest(r), sql, params)
	if !h.writeErrOrEmpty(w, resp, err, "TracesHandler.Search") {
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

// buildTraceSpansSQL returns every span of one trace, oldest-first, for the
// waterfall + span-detail panel. Pure; traceId is bound, not concatenated.
func buildTraceSpansSQL() (string, map[string]string) {
	sql := `
		SELECT
			TraceId AS trace_id,
			SpanId AS span_id,
			ParentSpanId AS parent_span_id,
			ServiceName AS service,
			SpanName AS operation,
			Timestamp AS start_time,
			Duration / 1000000.0 AS duration_ms,
			StatusCode AS status_code,
			SpanAttributes AS attributes
		FROM pulsetrace.otel_traces
		WHERE ResourceAttributes['tenant.id'] = {tenant:String}
		  AND TraceId = {traceId:String}
		ORDER BY Timestamp ASC
		LIMIT 2000
		FORMAT JSON
	`
	return sql, map[string]string{}
}

// GetTrace handles GET /api/v1/traces/{id} — all spans for one trace.
func (h *TracesHandler) GetTrace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	traceID := strings.TrimSpace(r.PathValue("id"))
	if traceID == "" {
		http.Error(w, "trace id is required", http.StatusBadRequest)
		return
	}

	sql, params := buildTraceSpansSQL()
	params["traceId"] = traceID
	resp, err := h.ch.queryScoped(tenantFromRequest(r), sql, params)
	if !h.writeErrOrEmpty(w, resp, err, "TracesHandler.GetTrace") {
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

// writeErrOrEmpty mirrors ServiceHandler's ClickHouse response handling: a 404
// (missing table) becomes an empty result, other non-200s are 500s.
func (h *TracesHandler) writeErrOrEmpty(w http.ResponseWriter, resp *http.Response, err error, logPrefix string) bool {
	if err != nil {
		log.Printf("[%s] query failed: %v", logPrefix, err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return false
	}
	if resp.StatusCode == http.StatusNotFound {
		io.WriteString(w, `{"data": []}`)
		return false
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[%s] ClickHouse returned %d: %s", logPrefix, resp.StatusCode, string(body))
		http.Error(w, "analytics engine returned error", http.StatusInternalServerError)
		return false
	}
	return true
}
