package handler

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/pulsetrace/gateway-service/internal/auth"
)

// SavedSearchHandler is the CRUD surface for named, reusable log/trace queries,
// following the same DB-backed/no-redeploy pattern as alert rules and rate-limit
// rules. Unlike those (which are tenant-wide admin config), a saved search is
// owned by the user who created it: it's scoped to the gateway-verified JWT
// subject, and only the owner can modify or delete one. A search can be marked
// `shared` to make it visible (read-only) to the rest of the tenant.
type SavedSearchHandler struct {
	db *sql.DB
}

func NewSavedSearchHandler(db *sql.DB) *SavedSearchHandler {
	return &SavedSearchHandler{db: db}
}

type savedSearchRow struct {
	ID          string            `json:"id"`
	TenantID    string            `json:"tenant_id"`
	Owner       string            `json:"owner"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	QueryParams map[string]string `json:"query_params"`
	Shared      bool              `json:"shared"`
	// Mine tells the client whether the caller owns this row (and may therefore
	// edit/delete it), which matters for shared searches surfaced from teammates.
	Mine bool `json:"mine"`
}

var validSearchKinds = map[string]bool{"logs": true, "traces": true}

// ownerOf returns the gateway-verified subject of the caller. The gateway sets
// X-User-Subject strictly from signed JWT claims (see auth.go), so it's safe to
// trust here as the row owner — it is never taken from the request body.
func ownerOf(r *http.Request) string {
	return r.Header.Get("X-User-Subject")
}

// List handles GET /api/v1/saved-searches — the caller's own searches plus any
// searches shared within their tenant. An optional ?kind=logs|traces narrows the
// result to one surface, so the log explorer and trace view each see only their
// own saved searches.
func (h *SavedSearchHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	tenantID := tenantFromRequest(r)
	owner := ownerOf(r)

	// kind is an optional filter; an empty/absent value matches every kind. It's
	// validated so an unknown value can't silently return nothing.
	kindFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kindFilter != "" && !validSearchKinds[kindFilter] {
		http.Error(w, "kind must be one of logs, traces", http.StatusBadRequest)
		return
	}

	rows, err := h.db.Query(`
		SELECT id, tenant_id, owner, name, kind, query_params, shared
		FROM saved_searches
		WHERE tenant_id = $1 AND (owner = $2 OR shared = true)
		  AND ($3 = '' OR kind = $3)
		ORDER BY name ASC`, tenantID, owner, kindFilter)
	if err != nil {
		log.Printf("saved_searches: failed to list: %v", err)
		http.Error(w, "failed to list saved searches", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []savedSearchRow{}
	for rows.Next() {
		var s savedSearchRow
		var params []byte
		if err := rows.Scan(&s.ID, &s.TenantID, &s.Owner, &s.Name, &s.Kind, &params, &s.Shared); err != nil {
			continue
		}
		if len(params) > 0 {
			_ = json.Unmarshal(params, &s.QueryParams)
		}
		if s.QueryParams == nil {
			s.QueryParams = map[string]string{}
		}
		s.Mine = s.Owner == owner
		out = append(out, s)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}

type upsertSavedSearchRequest struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	QueryParams map[string]string `json:"query_params"`
	Shared      *bool             `json:"shared"`
}

// normalizeKind lower-cases and defaults the kind, returning ok=false for an
// unrecognised value so the handler can 400.
func normalizeKind(kind string) (string, bool) {
	if kind == "" {
		return "logs", true
	}
	kind = strings.ToLower(kind)
	return kind, validSearchKinds[kind]
}

// Create handles POST /api/v1/saved-searches.
func (h *SavedSearchHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req upsertSavedSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	kind, ok := normalizeKind(req.Kind)
	if !ok {
		http.Error(w, "kind must be one of logs, traces", http.StatusBadRequest)
		return
	}
	owner := ownerOf(r)
	if owner == "" {
		// Without a subject we can't attribute ownership; this only happens if
		// the route is somehow reached unauthenticated.
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}

	params, err := json.Marshal(req.QueryParams)
	if err != nil {
		http.Error(w, "invalid query_params", http.StatusBadRequest)
		return
	}
	shared := req.Shared != nil && *req.Shared

	var id string
	err = h.db.QueryRow(
		`INSERT INTO saved_searches (tenant_id, owner, name, kind, query_params, shared)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		tenantFromRequest(r), owner, req.Name, kind, params, shared,
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			http.Error(w, "a saved search with that name already exists", http.StatusConflict)
			return
		}
		log.Printf("saved_searches: failed to create: %v", err)
		http.Error(w, "failed to create saved search", http.StatusInternalServerError)
		return
	}
	auth.WriteAudit(h.db, actorFromHeader(r), "create", "saved_search", req.Name, nil, req)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "created", "id": id})
}

// Update handles PUT /api/v1/saved-searches/{id}. Ownership is enforced in the
// WHERE clause: a user (even an editor) can't modify another user's search
// through this endpoint, only their own.
func (h *SavedSearchHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req upsertSavedSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Kind != "" {
		if _, ok := normalizeKind(req.Kind); !ok {
			http.Error(w, "kind must be one of logs, traces", http.StatusBadRequest)
			return
		}
	}

	// query_params is replaced wholesale when present; a nil map means "leave
	// unchanged", an empty map means "clear the filters".
	var paramsArg interface{}
	if req.QueryParams != nil {
		b, err := json.Marshal(req.QueryParams)
		if err != nil {
			http.Error(w, "invalid query_params", http.StatusBadRequest)
			return
		}
		paramsArg = b
	}
	shared := false
	if req.Shared != nil {
		shared = *req.Shared
	}

	res, err := h.db.Exec(
		`UPDATE saved_searches SET
			name = COALESCE(NULLIF($1, ''), name),
			kind = COALESCE(NULLIF($2, ''), kind),
			query_params = COALESCE($3, query_params),
			shared = $4,
			updated_at = now()
		WHERE id = $5 AND tenant_id = $6 AND owner = $7`,
		req.Name, strings.ToLower(req.Kind), paramsArg, shared, id, tenantFromRequest(r), ownerOf(r),
	)
	if err != nil {
		log.Printf("saved_searches: failed to update: %v", err)
		http.Error(w, "failed to update saved search", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "saved search not found", http.StatusNotFound)
		return
	}
	auth.WriteAudit(h.db, actorFromHeader(r), "update", "saved_search", id, nil, req)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// Delete handles DELETE /api/v1/saved-searches/{id}, ownership-enforced.
func (h *SavedSearchHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := h.db.Exec(
		"DELETE FROM saved_searches WHERE id = $1 AND tenant_id = $2 AND owner = $3",
		id, tenantFromRequest(r), ownerOf(r),
	)
	if err != nil {
		log.Printf("saved_searches: failed to delete: %v", err)
		http.Error(w, "failed to delete saved search", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "saved search not found", http.StatusNotFound)
		return
	}
	auth.WriteAudit(h.db, actorFromHeader(r), "delete", "saved_search", id, nil, nil)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}
