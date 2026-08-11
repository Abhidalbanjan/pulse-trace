package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// SCIM 2.0 user provisioning (F18, RFC 7644).
//
// Enterprise identity providers (Okta, Azure AD, OneLogin) manage user
// lifecycle by pushing to a SCIM endpoint: create on hire, deactivate on
// offboard. This implements the Users resource over the existing users table so
// a directory sync can provision/deprovision PulseTrace accounts, which then
// authenticate via the existing OIDC SSO.
//
// Auth is a static bearer token (SCIM_TOKEN) — the mechanism every IdP's SCIM
// client speaks — compared in constant time. With no token configured the
// endpoint is disabled (503), never open.

const (
	scimUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimContentType = "application/scim+json"
)

// SCIMHandler serves the SCIM Users resource.
type SCIMHandler struct {
	db       *sql.DB
	sessions *SessionStore
	token    string
	tenantID string
}

func NewSCIMHandler(db *sql.DB, sessions *SessionStore) *SCIMHandler {
	tenant := strings.TrimSpace(os.Getenv("SCIM_TENANT_ID"))
	if tenant == "" {
		tenant = defaultTenantID
	}
	return &SCIMHandler{
		db:       db,
		sessions: sessions,
		token:    strings.TrimSpace(os.Getenv("SCIM_TOKEN")),
		tenantID: tenant,
	}
}

// Enabled reports whether SCIM is configured (a token is set).
func (h *SCIMHandler) Enabled() bool { return h.token != "" }

// authorize enforces the SCIM bearer token in constant time. Returns false and
// writes the response when unauthorized or disabled.
func (h *SCIMHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if !h.Enabled() {
		h.scimError(w, http.StatusServiceUnavailable, "SCIM provisioning is not enabled on this server")
		return false
	}
	presented := bearerToken(r)
	if subtle.ConstantTimeCompare([]byte(presented), []byte(h.token)) != 1 {
		h.scimError(w, http.StatusUnauthorized, "invalid SCIM credentials")
		return false
	}
	return true
}

// ── SCIM resource shapes ───────────────────────────────────────────────────

type scimName struct {
	Formatted string `json:"formatted,omitempty"`
}

type scimEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
}

type scimUser struct {
	Schemas    []string    `json:"schemas"`
	ID         string      `json:"id,omitempty"`
	ExternalID string      `json:"externalId,omitempty"`
	UserName   string      `json:"userName"`
	Active     bool        `json:"active"`
	Name       *scimName   `json:"name,omitempty"`
	Emails     []scimEmail `json:"emails,omitempty"`
	Meta       *scimMeta   `json:"meta,omitempty"`
}

type scimMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	Location     string `json:"location,omitempty"`
}

type scimListResponse struct {
	Schemas      []string   `json:"schemas"`
	TotalResults int        `json:"totalResults"`
	StartIndex   int        `json:"startIndex"`
	ItemsPerPage int        `json:"itemsPerPage"`
	Resources    []scimUser `json:"Resources"`
}

// dbUser is the internal projection used to render a scimUser.
type dbUser struct {
	id         string
	username   string
	externalID sql.NullString
	active     bool
	createdAt  sql.NullString
}

func (u dbUser) toSCIM() scimUser {
	su := scimUser{
		Schemas:    []string{scimUserSchema},
		ID:         u.id,
		ExternalID: u.externalID.String,
		UserName:   u.username,
		Active:     u.active,
		Emails:     []scimEmail{{Value: u.username, Primary: true}},
		Meta:       &scimMeta{ResourceType: "User", Location: "/scim/v2/Users/" + u.id},
	}
	if u.createdAt.Valid {
		su.Meta.Created = u.createdAt.String
	}
	return su
}

// ── Handlers ───────────────────────────────────────────────────────────────

// ServeUsers routes /scim/v2/Users (collection: GET list, POST create).
func (h *SCIMHandler) ServeUsers(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.list(w, r)
	case http.MethodPost:
		h.create(w, r)
	default:
		h.scimError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ServeUser routes /scim/v2/Users/{id} (GET, PUT replace, PATCH, DELETE).
func (h *SCIMHandler) ServeUser(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		h.get(w, r, id)
	case http.MethodPut:
		h.replace(w, r, id)
	case http.MethodPatch:
		h.patch(w, r, id)
	case http.MethodDelete:
		h.deactivate(w, r, id, true)
	default:
		h.scimError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *SCIMHandler) list(w http.ResponseWriter, r *http.Request) {
	// Support the one filter IdPs actually send: userName eq "value".
	var rows *sql.Rows
	var err error
	if uname, ok := parseUserNameFilter(r.URL.Query().Get("filter")); ok {
		rows, err = h.db.QueryContext(r.Context(),
			`SELECT id, username, external_id, active, created_at::text FROM users WHERE tenant_id = $1 AND username = $2`,
			h.tenantID, uname)
	} else {
		rows, err = h.db.QueryContext(r.Context(),
			`SELECT id, username, external_id, active, created_at::text FROM users WHERE tenant_id = $1 ORDER BY username LIMIT 500`,
			h.tenantID)
	}
	if err != nil {
		h.scimError(w, http.StatusInternalServerError, "failed to list users")
		return
	}
	defer rows.Close()

	resources := []scimUser{}
	for rows.Next() {
		var u dbUser
		if err := rows.Scan(&u.id, &u.username, &u.externalID, &u.active, &u.createdAt); err != nil {
			continue
		}
		resources = append(resources, u.toSCIM())
	}
	h.writeSCIM(w, http.StatusOK, scimListResponse{
		Schemas:      []string{scimListSchema},
		TotalResults: len(resources),
		StartIndex:   1,
		ItemsPerPage: len(resources),
		Resources:    resources,
	})
}

func (h *SCIMHandler) get(w http.ResponseWriter, r *http.Request, id string) {
	u, err := h.loadUser(r, id)
	if err != nil {
		h.scimError(w, http.StatusNotFound, "user not found")
		return
	}
	h.writeSCIM(w, http.StatusOK, u.toSCIM())
}

func (h *SCIMHandler) create(w http.ResponseWriter, r *http.Request) {
	// A create with "active" omitted defaults to active:true, so decode into a
	// shape that distinguishes "false" from "absent".
	var in struct {
		UserName   string `json:"userName"`
		ExternalID string `json:"externalId"`
		Active     *bool  `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.UserName) == "" {
		h.scimError(w, http.StatusBadRequest, "userName is required")
		return
	}
	username := strings.TrimSpace(in.UserName)

	// Idempotency: if the user already exists (re-sync), return it rather than
	// 500-ing on the unique constraint — SCIM clients retry.
	if existing, err := h.loadUserByName(r, username); err == nil {
		h.writeSCIM(w, http.StatusOK, existing.toSCIM())
		return
	}

	// SCIM users authenticate via SSO, so store a random unusable password hash
	// (they never log in with a password), mirroring the SSO auto-provision path.
	dummy := make([]byte, 24)
	_, _ = rand.Read(dummy)
	pwHash, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(dummy)), bcrypt.DefaultCost)
	if err != nil {
		h.scimError(w, http.StatusInternalServerError, "failed to provision user")
		return
	}

	id := uuid.NewString()
	active := in.Active == nil || *in.Active // default active:true when omitted
	if _, err := h.db.ExecContext(r.Context(),
		`INSERT INTO users (id, username, password_hash, role, tenant_id, tier, active, external_id)
		 VALUES ($1, $2, $3, 'viewer', $4, 'standard', $5, $6)`,
		id, username, string(pwHash), h.tenantID, active, nullIfEmpty(in.ExternalID)); err != nil {
		h.scimError(w, http.StatusConflict, "failed to create user")
		return
	}
	WriteAudit(h.db, "scim", "create", "user", username, nil, map[string]interface{}{"external_id": in.ExternalID, "active": active})

	u, _ := h.loadUser(r, id)
	h.writeSCIM(w, http.StatusCreated, u.toSCIM())
}

func (h *SCIMHandler) replace(w http.ResponseWriter, r *http.Request, id string) {
	var in scimUser
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		h.scimError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if _, err := h.loadUser(r, id); err != nil {
		h.scimError(w, http.StatusNotFound, "user not found")
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET active = $1, external_id = COALESCE($2, external_id) WHERE id = $3 AND tenant_id = $4`,
		in.Active, nullIfEmpty(in.ExternalID), id, h.tenantID); err != nil {
		h.scimError(w, http.StatusInternalServerError, "failed to update user")
		return
	}
	if !in.Active {
		h.revokeUserSessions(r, id)
	}
	WriteAudit(h.db, "scim", "update", "user", id, nil, map[string]interface{}{"active": in.Active})
	u, _ := h.loadUser(r, id)
	h.writeSCIM(w, http.StatusOK, u.toSCIM())
}

// patch handles the one operation IdPs use for deprovisioning: set active.
func (h *SCIMHandler) patch(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.scimError(w, http.StatusBadRequest, "invalid PATCH body")
		return
	}
	if _, err := h.loadUser(r, id); err != nil {
		h.scimError(w, http.StatusNotFound, "user not found")
		return
	}

	active, changed := activeFromPatch(body.Operations)
	if changed {
		if _, err := h.db.ExecContext(r.Context(),
			`UPDATE users SET active = $1 WHERE id = $2 AND tenant_id = $3`, active, id, h.tenantID); err != nil {
			h.scimError(w, http.StatusInternalServerError, "failed to update user")
			return
		}
		if !active {
			h.revokeUserSessions(r, id)
		}
		WriteAudit(h.db, "scim", "update", "user", id, nil, map[string]interface{}{"active": active})
	}
	u, _ := h.loadUser(r, id)
	h.writeSCIM(w, http.StatusOK, u.toSCIM())
}

// deactivate handles DELETE as a soft-delete (SCIM DELETE means deprovision):
// mark inactive and revoke sessions rather than dropping the row and its audit
// trail.
func (h *SCIMHandler) deactivate(w http.ResponseWriter, r *http.Request, id string, _ bool) {
	res, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET active = false WHERE id = $1 AND tenant_id = $2`, id, h.tenantID)
	if err != nil {
		h.scimError(w, http.StatusInternalServerError, "failed to deactivate user")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		h.scimError(w, http.StatusNotFound, "user not found")
		return
	}
	h.revokeUserSessions(r, id)
	WriteAudit(h.db, "scim", "delete", "user", id, nil, map[string]string{"action": "deactivated"})
	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ────────────────────────────────────────────────────────────────

func (h *SCIMHandler) revokeUserSessions(r *http.Request, id string) {
	var username string
	if err := h.db.QueryRowContext(r.Context(), "SELECT username FROM users WHERE id = $1", id).Scan(&username); err == nil {
		if err := h.sessions.RevokeAllForUser(r.Context(), username); err != nil {
			log.Printf("scim: failed to revoke sessions for %s: %v", username, err)
		}
	}
}

func (h *SCIMHandler) loadUser(r *http.Request, id string) (dbUser, error) {
	var u dbUser
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, username, external_id, active, created_at::text FROM users WHERE id = $1 AND tenant_id = $2`,
		id, h.tenantID).Scan(&u.id, &u.username, &u.externalID, &u.active, &u.createdAt)
	return u, err
}

func (h *SCIMHandler) loadUserByName(r *http.Request, username string) (dbUser, error) {
	var u dbUser
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, username, external_id, active, created_at::text FROM users WHERE username = $1 AND tenant_id = $2`,
		username, h.tenantID).Scan(&u.id, &u.username, &u.externalID, &u.active, &u.createdAt)
	return u, err
}

func (h *SCIMHandler) writeSCIM(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *SCIMHandler) scimError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"schemas": []string{scimErrorSchema},
		"detail":  detail,
		"status":  status,
	})
}

func nullIfEmpty(s string) interface{} {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// parseUserNameFilter extracts the value from the SCIM filter IdPs send on
// lookup: `userName eq "value"` (case-insensitive attribute/operator). Returns
// ("", false) for any other or absent filter, in which case the caller lists.
func parseUserNameFilter(filter string) (string, bool) {
	f := strings.TrimSpace(filter)
	if f == "" {
		return "", false
	}
	lower := strings.ToLower(f)
	if !strings.HasPrefix(lower, "username eq ") {
		return "", false
	}
	rest := strings.TrimSpace(f[len("userName eq "):])
	if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
		return rest[1 : len(rest)-1], true
	}
	return "", false
}

// activeFromPatch scans a SCIM PATCH for a set of the `active` attribute — the
// deprovision operation. Returns the target value and whether it was present.
// It tolerates both `path: "active"` and a valueless replace of the whole
// resource carrying {"active": ...}.
func activeFromPatch(ops []struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}) (active bool, changed bool) {
	for _, op := range ops {
		if !strings.EqualFold(op.Op, "replace") && !strings.EqualFold(op.Op, "add") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(op.Path), "active") {
			var b bool
			if json.Unmarshal(op.Value, &b) == nil {
				return b, true
			}
		}
		if op.Path == "" {
			var obj struct {
				Active *bool `json:"active"`
			}
			if json.Unmarshal(op.Value, &obj) == nil && obj.Active != nil {
				return *obj.Active, true
			}
		}
	}
	return false, false
}
