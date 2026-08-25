package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/pulsetrace/gateway-service/internal/sqlq"
)

// SQLQueryHandler serves POST /api/v1/query/sql — user-authored SQL (P3.2).
//
// The isolation guarantee lives in internal/sqlq, not here: this handler's only
// security responsibility is to pass the *gateway-verified* tenant and nothing
// else. AuthMiddleware strips X-Tenant-ID from every inbound request and re-sets
// it only from signed JWT claims or a resolved ingestion key, so
// tenantFromRequest is a trusted source; the engine takes the tenant as an
// argument and never reads it from the statement.
type SQLQueryHandler struct {
	db     *sql.DB
	engine *sqlq.Engine
}

func NewSQLQueryHandler(db *sql.DB, engine *sqlq.Engine) *SQLQueryHandler {
	return &SQLQueryHandler{db: db, engine: engine}
}

type sqlQueryRequest struct {
	SQL string `json:"sql"`
}

// maxRequestBytes bounds the request body independently of the statement-size
// policy in sqlq. The policy runs after decoding; this stops a caller streaming
// a gigabyte at us before anything has had a chance to refuse it.
const maxRequestBytes = 1 << 20 // 1 MiB

// Execute runs one statement and streams the result as NDJSON.
//
// The response is three kinds of line, in order:
//
//	{"columns": [...]}          exactly once, first
//	{"row": [...]}              zero or more
//	{"stats": {...}}            exactly once, last
//
// NDJSON rather than a single JSON document because a result set has no useful
// upper bound and a client should be able to start rendering, and to stop
// reading, without holding the whole thing. Note the honest limit: sqlq
// materialises the result before returning it, so this streams the *response*,
// not the computation. Incremental execution is a later change and pretending
// otherwise here would misrepresent the memory profile.
func (h *SQLQueryHandler) Execute(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantFromRequest(r)
	actor := ownerOf(r)
	started := time.Now()

	var req sqlQueryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "body must be JSON: {\"sql\": \"...\"}", http.StatusBadRequest)
		return
	}
	statement := strings.TrimSpace(req.SQL)
	if statement == "" {
		http.Error(w, "sql is required", http.StatusBadRequest)
		return
	}

	result, err := h.engine.Query(r.Context(), tenantID, statement)
	if err != nil {
		h.reject(w, r, tenantID, actor, statement, started, err)
		return
	}

	// Headers before the first write; after that the status is already sent and
	// an error can only be reported inside the stream.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)

	if err := enc.Encode(map[string]any{"columns": result.Columns}); err != nil {
		return // client went away
	}
	for _, row := range result.Rows {
		// A cancelled request stops the stream rather than finishing a body
		// nobody is reading.
		if r.Context().Err() != nil {
			break
		}
		if err := enc.Encode(map[string]any{"row": row}); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	_ = enc.Encode(map[string]any{"stats": map[string]any{
		"rows_returned": len(result.Rows),
		"rows_scanned":  result.Scanned,
		"duration_ms":   result.Elapsed.Milliseconds(),
	}})
	if flusher != nil {
		flusher.Flush()
	}

	h.audit(r, tenantID, actor, statement, result.Relations, "ok", "",
		result.Scanned, len(result.Rows), time.Since(started))
}

// Schema serves GET /api/v1/query/schema — the relations a statement may name.
//
// Unauthenticated callers never reach this (the route sits behind the same auth
// as Execute), but note what it does *not* depend on: the tenant. The catalog
// is the same for every tenant because it describes the shape of the data, not
// any of it. Returning it per-tenant would imply a per-tenant schema we do not
// have, and inviting a tenant id into this path would create a parameter that
// looks like it should affect the answer.
func (h *SQLQueryHandler) Schema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// The catalog changes only on deploy, so a short cache is safe and keeps an
	// editor that refetches on focus from hitting the gateway repeatedly.
	w.Header().Set("Cache-Control", "private, max-age=60")
	_ = json.NewEncoder(w).Encode(map[string]any{"relations": h.engine.Schema()})
}

// reject maps an engine error onto a status code and records it.
//
// A policy refusal is 400: the user asked something they may not ask, and the
// reason is theirs to see so they can fix the query. A budget overrun is 413 —
// the request was legitimate and too large, which is a different conversation.
// Anything else is 500 and the detail stays in the log, because an internal
// failure's message can describe schema the caller should not learn about.
func (h *SQLQueryHandler) reject(w http.ResponseWriter, r *http.Request, tenantID, actor, statement string, started time.Time, err error) {
	var (
		status  = http.StatusInternalServerError
		outcome = "error"
		reason  string
		body    = "query failed"
	)

	if rs, ok := sqlq.ReasonOf(err); ok {
		status, outcome, reason, body = http.StatusBadRequest, "rejected", string(rs), err.Error()
	} else {
		var be *sqlq.BudgetError
		if errors.As(err, &be) {
			status, outcome, reason, body = http.StatusRequestEntityTooLarge, "rejected", "budget:"+be.Limit, err.Error()
		} else {
			log.Printf("query/sql: tenant=%s actor=%s: %v", tenantID, actor, err)
		}
	}

	h.audit(r, tenantID, actor, statement, nil, outcome, reason, 0, 0, time.Since(started))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body, "reason": reason})
}

// audit records the execution. Failures to record are logged and swallowed:
// losing an audit row is bad, but failing a query the user was entitled to run
// because the audit table is unavailable is worse, and the log preserves the
// event either way.
func (h *SQLQueryHandler) audit(r *http.Request, tenantID, actor, statement string, relations []string,
	outcome, reason string, scanned, returned int, elapsed time.Duration) {
	if h.db == nil {
		return
	}
	// pq.Array(nil) marshals to NULL, and query_audit.relations is NOT NULL.
	// A refusal has no resolved relations, so this is the common path, not the
	// edge case: every rejected query failed to audit until this was fixed.
	if relations == nil {
		relations = []string{}
	}
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO query_audit
		  (tenant_id, actor, statement, relations, rows_scanned, rows_returned, duration_ms, outcome, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''))`,
		tenantID, actor, statement, pq.Array(relations), scanned, returned, elapsed.Milliseconds(), outcome, reason)
	if err != nil {
		log.Printf("query/sql: failed to record audit for tenant=%s actor=%s: %v", tenantID, actor, err)
	}
}
