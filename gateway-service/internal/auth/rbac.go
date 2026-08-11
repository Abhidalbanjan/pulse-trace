package auth

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/expr-lang/expr"
)

// Role is a data-driven RBAC role: a name mapped to a set of permission
// strings. "*" grants every permission. Roles are rows in Postgres, not
// hardcoded Go constants, so operators can add/edit roles without a redeploy.
type Role struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	IsSystem    bool     `json:"is_system"`
}

// Policy is an ABAC rule: if Condition evaluates true for a given request's
// subject/resource/action attributes, Effect (allow/deny) is applied.
// Conditions are expr-lang expressions, e.g. `subject.role == "viewer" && action != "read"`.
type Policy struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Effect    string `json:"effect"` // "allow" | "deny"
	Resource  string `json:"resource"`
	Condition string `json:"condition"`
	Priority  int    `json:"priority"`
	Enabled   bool   `json:"enabled"`
}

// RBACEngine evaluates both RBAC (does this role have this permission?) and
// ABAC (do this request's attributes satisfy every applicable policy?) on
// every request. Roles/policies are cached in memory with a short TTL - genuinely
// dynamic (edits take effect within RefreshInterval, no redeploy) without hitting
// Postgres on every single request.
type RBACEngine struct {
	db              *sql.DB
	mu              sync.RWMutex
	roles           map[string]Role
	policies        []Policy
	lastRefresh     time.Time
	RefreshInterval time.Duration
}

func NewRBACEngine(db *sql.DB) *RBACEngine {
	e := &RBACEngine{db: db, RefreshInterval: 5 * time.Second}
	e.refresh()
	return e
}

func (e *RBACEngine) refresh() {
	roles := map[string]Role{}
	if e.db != nil {
		rows, err := e.db.Query("SELECT name, COALESCE(description, ''), permissions, is_system FROM roles")
		if err != nil {
			log.Printf("rbac: failed to load roles: %v", err)
		} else {
			for rows.Next() {
				var r Role
				var permsJSON []byte
				if err := rows.Scan(&r.Name, &r.Description, &permsJSON, &r.IsSystem); err != nil {
					continue
				}
				_ = json.Unmarshal(permsJSON, &r.Permissions)
				roles[r.Name] = r
			}
			rows.Close()
		}
	}
	// Always have a hard-coded admin fallback so a Postgres outage can't lock
	// operators out entirely, while everything else stays fully data-driven.
	if _, ok := roles["admin"]; !ok {
		roles["admin"] = Role{Name: "admin", Description: "fallback", Permissions: []string{"*"}, IsSystem: true}
	}

	var policies []Policy
	if e.db != nil {
		// ORDER BY here + the stable, fully-deterministic tie-break below are both required:
		// without a SQL ORDER BY, Postgres row order is itself unspecified across calls: two
		// enabled policies with the same priority could evaluate in a different order every
		// 5s refresh, silently flipping an allow/deny outcome for identical input.
		rows, err := e.db.Query("SELECT id, name, effect, resource, condition, priority, enabled FROM abac_policies WHERE enabled = true ORDER BY priority ASC, name ASC, id ASC")
		if err != nil {
			log.Printf("rbac: failed to load abac policies: %v", err)
		} else {
			for rows.Next() {
				var p Policy
				if err := rows.Scan(&p.ID, &p.Name, &p.Effect, &p.Resource, &p.Condition, &p.Priority, &p.Enabled); err != nil {
					continue
				}
				policies = append(policies, p)
			}
			rows.Close()
		}
	}
	// Belt-and-suspenders: re-sort deterministically even though the query above already
	// orders rows, so behavior doesn't depend on the SQL ORDER BY staying in sync with this.
	sort.SliceStable(policies, func(i, j int) bool {
		if policies[i].Priority != policies[j].Priority {
			return policies[i].Priority < policies[j].Priority
		}
		if policies[i].Name != policies[j].Name {
			return policies[i].Name < policies[j].Name
		}
		return policies[i].ID < policies[j].ID
	})

	e.mu.Lock()
	e.roles = roles
	e.policies = policies
	e.lastRefresh = time.Now()
	e.mu.Unlock()
}

func (e *RBACEngine) refreshIfStale() {
	e.mu.RLock()
	stale := time.Since(e.lastRefresh) > e.RefreshInterval
	e.mu.RUnlock()
	if stale {
		e.refresh()
	}
}

// HasPermission reports whether role grants access to resourceType:action.
// Permission strings support four forms, checked most-specific first:
//   - "*"                       grants everything (built-in admin role)
//   - "<resourceType>:<action>" exact resource+action, e.g. "incidents:write"
//   - "<resourceType>:*"        every action on one resource type
//   - "*:<action>"              one action across every resource type
//   - "<action>" (bare)         legacy/back-compat form, equivalent to "*:<action>" -
//     this is what lets the original seeded viewer ("read") and editor
//     ("read","write") roles keep working without a data migration.
//
// Without resource scoping, any role granted "write" could write to every
// resource including admin/settings/roles/policies - this is what makes that
// no longer true for newly-scoped permissions like "incidents:write".
func (e *RBACEngine) HasPermission(role, resourceType, action string) bool {
	e.refreshIfStale()
	e.mu.RLock()
	defer e.mu.RUnlock()
	r, ok := e.roles[role]
	if !ok {
		return false
	}
	for _, p := range r.Permissions {
		switch p {
		case "*", action, resourceType + ":*", "*:" + action, resourceType + ":" + action:
			return true
		}
	}
	return false
}

// Roles returns a snapshot of every configured role.
func (e *RBACEngine) Roles() []Role {
	e.refreshIfStale()
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Role, 0, len(e.roles))
	for _, r := range e.roles {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Policies returns a snapshot of every configured ABAC policy (enabled or not -
// callers managing policies need to see disabled ones too).
func (e *RBACEngine) Policies() []Policy {
	if e.db == nil {
		return nil
	}
	rows, err := e.db.Query("SELECT id, name, effect, resource, condition, priority, enabled FROM abac_policies ORDER BY priority ASC, name ASC, id ASC")
	if err != nil {
		log.Printf("rbac: failed to list abac policies: %v", err)
		return nil
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		var p Policy
		if err := rows.Scan(&p.ID, &p.Name, &p.Effect, &p.Resource, &p.Condition, &p.Priority, &p.Enabled); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// abacValidationEnv returns the representative attribute environment a policy
// condition is authored against: the same shape EvaluateABAC builds at request
// time (subject.role/tenant_id/tier, resource.type, action). A condition that
// compiles against this env is one that can actually run.
func abacValidationEnv() map[string]interface{} {
	return map[string]interface{}{
		"subject":  map[string]interface{}{"role": "", "tenant_id": "", "tier": ""},
		"resource": map[string]interface{}{"type": ""},
		"action":   "",
	}
}

// ValidateCondition compiles an expr-lang policy condition against the
// representative env and returns a descriptive error if it is malformed or not
// boolean-typed. Compile-only: it never runs the expression, so it is safe to
// call on unsaved, operator-supplied input for live authoring feedback.
func ValidateCondition(condition string) error {
	_, err := expr.Compile(condition, expr.Env(abacValidationEnv()), expr.AsBool())
	return err
}

// evaluateCondition compiles and runs an expr-lang boolean expression against
// the subject/resource/action attributes. expr-lang is a sandboxed expression
// language (no side effects, no arbitrary code execution) - safe to evaluate
// operator-authored policy conditions at request time.
func evaluateCondition(condition string, env map[string]interface{}) (bool, error) {
	program, err := expr.Compile(condition, expr.Env(env), expr.AsBool())
	if err != nil {
		return false, err
	}
	out, err := expr.Run(program, env)
	if err != nil {
		return false, err
	}
	result, _ := out.(bool)
	return result, nil
}

// EvaluateABAC walks policies in priority order; the first enabled policy whose
// Resource matches (or is "*") AND whose Condition evaluates true decides the
// outcome. No match at all means allow - ABAC here adds constraints on top of
// the coarser RBAC permission check, it doesn't replace it.
func (e *RBACEngine) EvaluateABAC(resourceType, action string, subject map[string]interface{}) (allow bool, deniedBy string) {
	e.refreshIfStale()
	e.mu.RLock()
	policies := e.policies
	e.mu.RUnlock()

	env := map[string]interface{}{
		"subject":  subject,
		"resource": map[string]interface{}{"type": resourceType},
		"action":   action,
	}

	for _, p := range policies {
		if p.Resource != "*" && p.Resource != resourceType {
			continue
		}
		matched, err := evaluateCondition(p.Condition, env)
		if err != nil {
			log.Printf("rbac: policy %q condition failed to evaluate, skipping: %v", p.Name, err)
			continue
		}
		if !matched {
			continue
		}
		if strings.EqualFold(p.Effect, "deny") {
			return false, p.Name
		}
		return true, p.Name
	}
	return true, ""
}

// resourceTypeAndAction extracts the resource type (first path segment after
// /api/v1/ - "incidents", "admin", "settings", "topology", etc, so admin/settings
// are ordinary resource types now, not a special case) plus the finer ABAC action
// ("read"/"create"/"update"/"delete") from a request.
func resourceTypeAndAction(path, method string) (resourceType, abacAction string) {
	trimmed := strings.TrimPrefix(path, "/api/v1/")
	trimmed = strings.TrimPrefix(trimmed, "/")
	segments := strings.SplitN(trimmed, "/", 2)
	resourceType = segments[0]
	if resourceType == "" {
		resourceType = "root"
	}

	switch method {
	case http.MethodGet, http.MethodHead:
		abacAction = "read"
	case http.MethodPost:
		abacAction = "create"
	case http.MethodPut, http.MethodPatch:
		abacAction = "update"
	case http.MethodDelete:
		abacAction = "delete"
	default:
		abacAction = "write"
	}
	return resourceType, abacAction
}

// Middleware replaces the old hardcoded "admin-only /api/v1/admin" check with
// dynamic, resource-scoped RBAC (role -> "<resource>:<action>" permissions, from
// Postgres) followed by ABAC (attribute policies evaluated over subject/resource/
// action). Requests with no resolved role (AuthMiddleware's public-endpoint bypass
// list: healthz, login, telemetry ingestion, etc.) skip both checks entirely, same
// as before.
func (e *RBACEngine) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-User-Role")
		if role == "" {
			next.ServeHTTP(w, r)
			return
		}

		resourceType, abacAction := resourceTypeAndAction(r.URL.Path, r.Method)
		// RBAC checks the coarse read/write bucket per resource type; ABAC (below)
		// can still distinguish create/update/delete within "write" for nuanced policies.
		rbacAction := "write"
		if abacAction == "read" {
			rbacAction = "read"
		}

		if !e.HasPermission(role, resourceType, rbacAction) {
			http.Error(w, fmt.Sprintf("Forbidden: role %q lacks permission %q on %q", role, rbacAction, resourceType), http.StatusForbidden)
			return
		}

		subject := map[string]interface{}{
			"role":      role,
			"tenant_id": r.Header.Get("X-Tenant-ID"),
			"tier":      r.Header.Get("X-Tenant-Tier"),
		}
		if allow, deniedBy := e.EvaluateABAC(resourceType, abacAction, subject); !allow {
			http.Error(w, "Forbidden by policy: "+deniedBy, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ── Role & Policy management APIs (all admin-gated by Middleware itself,
// since these live under /api/v1/admin) ─────────────────────────────────

// ListRoles handles GET /api/v1/admin/roles
func (e *RBACEngine) ListRoles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": e.Roles()})
}

type upsertRoleRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

// CreateRole handles POST /api/v1/admin/roles
func (e *RBACEngine) CreateRole(w http.ResponseWriter, r *http.Request) {
	var req upsertRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || len(req.Permissions) == 0 {
		http.Error(w, "name and at least one permission are required", http.StatusBadRequest)
		return
	}
	permsJSON, _ := json.Marshal(req.Permissions)
	_, err := e.db.Exec(
		"INSERT INTO roles (name, description, permissions, is_system) VALUES ($1, $2, $3, false)",
		req.Name, req.Description, permsJSON,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			http.Error(w, "role already exists", http.StatusConflict)
			return
		}
		log.Printf("rbac: failed to create role: %v", err)
		http.Error(w, "failed to create role", http.StatusInternalServerError)
		return
	}
	e.refresh()
	WriteAudit(e.db, actorFromRequest(r), "create", "role", req.Name, nil, req)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

// UpdateRole handles PUT /api/v1/admin/roles/{name}
func (e *RBACEngine) UpdateRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req upsertRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Permissions) == 0 {
		http.Error(w, "at least one permission is required", http.StatusBadRequest)
		return
	}

	e.mu.RLock()
	before, hadBefore := e.roles[name]
	e.mu.RUnlock()

	permsJSON, _ := json.Marshal(req.Permissions)
	res, err := e.db.Exec(
		"UPDATE roles SET permissions = $1, description = COALESCE(NULLIF($2, ''), description), updated_at = now() WHERE name = $3",
		permsJSON, req.Description, name,
	)
	if err != nil {
		log.Printf("rbac: failed to update role: %v", err)
		http.Error(w, "failed to update role", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "role not found", http.StatusNotFound)
		return
	}
	e.refresh()
	var beforeLog interface{}
	if hadBefore {
		beforeLog = before
	}
	req.Name = name
	WriteAudit(e.db, actorFromRequest(r), "update", "role", name, beforeLog, req)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// DeleteRole handles DELETE /api/v1/admin/roles/{name}
func (e *RBACEngine) DeleteRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	e.mu.RLock()
	before, hadBefore := e.roles[name]
	e.mu.RUnlock()

	res, err := e.db.Exec("DELETE FROM roles WHERE name = $1 AND is_system = false", name)
	if err != nil {
		log.Printf("rbac: failed to delete role: %v", err)
		http.Error(w, "failed to delete role", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "role not found or is a built-in system role", http.StatusBadRequest)
		return
	}
	e.refresh()
	var beforeLog interface{}
	if hadBefore {
		beforeLog = before
	}
	WriteAudit(e.db, actorFromRequest(r), "delete", "role", name, beforeLog, nil)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// ListPolicies handles GET /api/v1/admin/policies
func (e *RBACEngine) ListPolicies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	policies := e.Policies()
	if policies == nil {
		policies = []Policy{}
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": policies})
}

type upsertPolicyRequest struct {
	Name      string `json:"name"`
	Effect    string `json:"effect"`
	Resource  string `json:"resource"`
	Condition string `json:"condition"`
	Priority  int    `json:"priority"`
	Enabled   *bool  `json:"enabled"`
}

// CreatePolicy handles POST /api/v1/admin/policies
func (e *RBACEngine) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	var req upsertPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Condition == "" {
		http.Error(w, "name and condition are required", http.StatusBadRequest)
		return
	}
	if req.Effect != "allow" && req.Effect != "deny" {
		req.Effect = "deny"
	}
	if req.Resource == "" {
		req.Resource = "*"
	}

	// Validate the condition compiles before persisting a policy that would
	// otherwise silently no-op (or worse, error) on every request.
	if err := ValidateCondition(req.Condition); err != nil {
		http.Error(w, "invalid condition expression: "+err.Error(), http.StatusBadRequest)
		return
	}

	_, err := e.db.Exec(
		"INSERT INTO abac_policies (name, effect, resource, condition, priority, enabled) VALUES ($1, $2, $3, $4, $5, true)",
		req.Name, req.Effect, req.Resource, req.Condition, req.Priority,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			http.Error(w, "policy already exists", http.StatusConflict)
			return
		}
		log.Printf("rbac: failed to create policy: %v", err)
		http.Error(w, "failed to create policy", http.StatusInternalServerError)
		return
	}
	e.refresh()
	WriteAudit(e.db, actorFromRequest(r), "create", "policy", req.Name, nil, req)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

// policyByID fetches one policy's current state directly from Postgres (not the
// enabled-only in-memory cache) - used to capture a true before-state for the audit log.
func (e *RBACEngine) policyByID(id string) (Policy, bool) {
	var p Policy
	err := e.db.QueryRow("SELECT id, name, effect, resource, condition, priority, enabled FROM abac_policies WHERE id = $1", id).
		Scan(&p.ID, &p.Name, &p.Effect, &p.Resource, &p.Condition, &p.Priority, &p.Enabled)
	return p, err == nil
}

// ValidatePolicy handles POST /api/v1/admin/policies/validate {condition} and
// reports whether the expr-lang condition compiles, so the policy-authoring UI
// can give live feedback (and surface the exact compiler error) before an
// operator commits a rule. Read-only: it never persists anything.
func (e *RBACEngine) ValidatePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Condition string `json:"condition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	resp := map[string]interface{}{"valid": true}
	if strings.TrimSpace(req.Condition) == "" {
		resp["valid"] = false
		resp["error"] = "condition is empty"
	} else if err := ValidateCondition(req.Condition); err != nil {
		resp["valid"] = false
		resp["error"] = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// UpdatePolicy handles PUT /api/v1/admin/policies/{id} (used to enable/disable
// or tweak priority without deleting and recreating).
func (e *RBACEngine) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req upsertPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	before, hadBefore := e.policyByID(id)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	res, err := e.db.Exec("UPDATE abac_policies SET enabled = $1, priority = COALESCE(NULLIF($2, 0), priority) WHERE id = $3", enabled, req.Priority, id)
	if err != nil {
		log.Printf("rbac: failed to update policy: %v", err)
		http.Error(w, "failed to update policy", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "policy not found", http.StatusNotFound)
		return
	}
	e.refresh()
	var beforeLog interface{}
	if hadBefore {
		beforeLog = before
	}
	after, _ := e.policyByID(id)
	WriteAudit(e.db, actorFromRequest(r), "update", "policy", id, beforeLog, after)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// DeletePolicy handles DELETE /api/v1/admin/policies/{id}
func (e *RBACEngine) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	before, hadBefore := e.policyByID(id)
	res, err := e.db.Exec("DELETE FROM abac_policies WHERE id = $1", id)
	if err != nil {
		log.Printf("rbac: failed to delete policy: %v", err)
		http.Error(w, "failed to delete policy", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "policy not found", http.StatusNotFound)
		return
	}
	e.refresh()
	var beforeLog interface{}
	if hadBefore {
		beforeLog = before
	}
	WriteAudit(e.db, actorFromRequest(r), "delete", "policy", id, beforeLog, nil)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}
