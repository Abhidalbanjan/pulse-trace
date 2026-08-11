package auth

import (
	"net/http"
	"testing"
	"time"
)

// newTestEngine builds an RBACEngine with roles/policies set directly and a
// fresh lastRefresh, so refreshIfStale() never tries to hit the (nil) DB
// during the test. This is safe because the test lives in package auth and
// can see unexported fields.
func newTestEngine(roles map[string]Role, policies []Policy) *RBACEngine {
	return &RBACEngine{
		roles:           roles,
		policies:        policies,
		lastRefresh:     time.Now(),
		RefreshInterval: time.Hour,
	}
}

func TestHasPermission(t *testing.T) {
	roles := map[string]Role{
		"admin":  {Name: "admin", Permissions: []string{"*"}},
		"editor": {Name: "editor", Permissions: []string{"read", "write"}}, // legacy bare form
		"viewer": {Name: "viewer", Permissions: []string{"incidents:read", "topology:*"}},
		"none":   {Name: "none", Permissions: []string{}},
	}
	e := newTestEngine(roles, nil)

	tests := []struct {
		name         string
		role         string
		resourceType string
		action       string
		want         bool
	}{
		{"admin wildcard grants anything", "admin", "settings", "delete", true},
		{"legacy bare permission still works", "editor", "incidents", "read", true},
		{"legacy bare permission covers unrelated resource too (back-compat)", "editor", "roles", "write", true},
		{"exact resource:action match", "viewer", "incidents", "read", true},
		{"exact resource:action mismatch on action", "viewer", "incidents", "write", false},
		{"resource wildcard grants every action on that resource", "viewer", "topology", "delete", true},
		{"no matching permission denies", "viewer", "settings", "write", false},
		{"empty permission set denies everything", "none", "incidents", "read", false},
		{"unknown role denies everything", "ghost", "incidents", "read", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.HasPermission(tt.role, tt.resourceType, tt.action)
			if got != tt.want {
				t.Errorf("HasPermission(%q, %q, %q) = %v, want %v", tt.role, tt.resourceType, tt.action, got, tt.want)
			}
		})
	}
}

func TestResourceTypeAndAction(t *testing.T) {
	tests := []struct {
		path             string
		method           string
		wantResourceType string
		wantAction       string
	}{
		{"/api/v1/incidents", http.MethodGet, "incidents", "read"},
		{"/api/v1/incidents/123", http.MethodPost, "incidents", "create"},
		{"/api/v1/admin/users/5/role", http.MethodPut, "admin", "update"},
		{"/api/v1/settings", http.MethodDelete, "settings", "delete"},
		{"/api/v1/", http.MethodGet, "root", "read"},
		{"/api/v1/topology/agent-config", http.MethodPatch, "topology", "update"},
		{"/api/v1/incidents", http.MethodOptions, "incidents", "write"}, // unrecognized method falls back to "write"
	}

	for _, tt := range tests {
		t.Run(tt.path+" "+tt.method, func(t *testing.T) {
			gotResource, gotAction := resourceTypeAndAction(tt.path, tt.method)
			if gotResource != tt.wantResourceType || gotAction != tt.wantAction {
				t.Errorf("resourceTypeAndAction(%q, %q) = (%q, %q), want (%q, %q)",
					tt.path, tt.method, gotResource, gotAction, tt.wantResourceType, tt.wantAction)
			}
		})
	}
}

func TestEvaluateCondition(t *testing.T) {
	env := map[string]interface{}{
		"subject": map[string]interface{}{"role": "viewer", "tenant_id": "acme"},
		"action":  "delete",
	}

	tests := []struct {
		name      string
		condition string
		want      bool
		wantErr   bool
	}{
		{"simple equality true", `subject.role == "viewer"`, true, false},
		{"simple equality false", `subject.role == "admin"`, false, false},
		{"compound condition", `subject.role == "viewer" && action == "delete"`, true, false},
		{"always true literal", "true", true, false},
		{"malformed expression errors instead of silently allowing", `subject.role ==`, false, true},
		{"unknown field errors instead of silently allowing", `subject.nonexistent_field.deeper == "x"`, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evaluateCondition(tt.condition, env)
			if (err != nil) != tt.wantErr {
				t.Fatalf("evaluateCondition(%q) error = %v, wantErr %v", tt.condition, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("evaluateCondition(%q) = %v, want %v", tt.condition, got, tt.want)
			}
		})
	}
}

func TestEvaluateABAC(t *testing.T) {
	t.Run("no policies configured defaults to allow", func(t *testing.T) {
		e := newTestEngine(nil, nil)
		allow, deniedBy := e.EvaluateABAC("incidents", "read", map[string]interface{}{"role": "viewer"})
		if !allow || deniedBy != "" {
			t.Errorf("got allow=%v deniedBy=%q, want allow=true deniedBy=\"\"", allow, deniedBy)
		}
	})

	t.Run("matching deny policy blocks the request", func(t *testing.T) {
		e := newTestEngine(nil, []Policy{
			{Name: "block-viewer-deletes", Effect: "deny", Resource: "incidents", Condition: `subject.role == "viewer" && action == "delete"`, Enabled: true, Priority: 1},
		})
		allow, deniedBy := e.EvaluateABAC("incidents", "delete", map[string]interface{}{"role": "viewer"})
		if allow || deniedBy != "block-viewer-deletes" {
			t.Errorf("got allow=%v deniedBy=%q, want allow=false deniedBy=block-viewer-deletes", allow, deniedBy)
		}
	})

	t.Run("deny policy for a different resource does not apply", func(t *testing.T) {
		e := newTestEngine(nil, []Policy{
			{Name: "block-settings", Effect: "deny", Resource: "settings", Condition: "true", Enabled: true, Priority: 1},
		})
		allow, _ := e.EvaluateABAC("incidents", "delete", map[string]interface{}{"role": "viewer"})
		if !allow {
			t.Errorf("policy scoped to a different resource should not have applied, got allow=false")
		}
	})

	t.Run("wildcard resource policy applies to everything", func(t *testing.T) {
		e := newTestEngine(nil, []Policy{
			{Name: "global-block", Effect: "deny", Resource: "*", Condition: `subject.tenant_id != "acme"`, Enabled: true, Priority: 1},
		})
		allow, deniedBy := e.EvaluateABAC("incidents", "read", map[string]interface{}{"tenant_id": "other-tenant"})
		if allow || deniedBy != "global-block" {
			t.Errorf("got allow=%v deniedBy=%q, want allow=false deniedBy=global-block", allow, deniedBy)
		}
	})

	t.Run("first matching policy wins, priority order matters", func(t *testing.T) {
		e := newTestEngine(nil, []Policy{
			{Name: "allow-first", Effect: "allow", Resource: "incidents", Condition: "true", Enabled: true, Priority: 1},
			{Name: "deny-second", Effect: "deny", Resource: "incidents", Condition: "true", Enabled: true, Priority: 2},
		})
		allow, deniedBy := e.EvaluateABAC("incidents", "read", map[string]interface{}{"role": "viewer"})
		if !allow || deniedBy != "allow-first" {
			t.Errorf("got allow=%v deniedBy=%q, want allow=true deniedBy=allow-first (first match should win)", allow, deniedBy)
		}
	})

	t.Run("a policy with a broken condition is skipped, not treated as a match", func(t *testing.T) {
		e := newTestEngine(nil, []Policy{
			{Name: "broken", Effect: "deny", Resource: "incidents", Condition: `subject.role ==`, Enabled: true, Priority: 1},
			{Name: "fallback-allow", Effect: "allow", Resource: "incidents", Condition: "true", Enabled: true, Priority: 2},
		})
		allow, deniedBy := e.EvaluateABAC("incidents", "read", map[string]interface{}{"role": "viewer"})
		if !allow || deniedBy != "fallback-allow" {
			t.Errorf("got allow=%v deniedBy=%q, want the broken policy skipped and fallback-allow to match", allow, deniedBy)
		}
	})

	// Migration 015's self-service carve-out: a higher-priority allow on the
	// saved-searches resource must beat the two seeded global deny policies, but
	// only for that resource. This models the exact seeded policy set.
	t.Run("saved-search self-service carve-out overrides global denies for that resource only", func(t *testing.T) {
		seeded := []Policy{
			{Name: "saved-searches-self-service", Effect: "allow", Resource: "saved-searches", Condition: "true", Enabled: true, Priority: 5},
			{Name: "no-non-admin-deletes", Effect: "deny", Resource: "*", Condition: `action == "delete" && subject.role != "admin"`, Enabled: true, Priority: 10},
			{Name: "viewer-strictly-read-only", Effect: "deny", Resource: "*", Condition: `subject.role == "viewer" && action != "read"`, Enabled: true, Priority: 20},
		}
		e := newTestEngine(nil, seeded)

		// A viewer creating and deleting their own saved search is allowed.
		if allow, deniedBy := e.EvaluateABAC("saved-searches", "create", map[string]interface{}{"role": "viewer"}); !allow {
			t.Errorf("viewer create saved-search should be allowed, denied by %q", deniedBy)
		}
		if allow, deniedBy := e.EvaluateABAC("saved-searches", "delete", map[string]interface{}{"role": "viewer"}); !allow {
			t.Errorf("viewer delete saved-search should be allowed, denied by %q", deniedBy)
		}
		// An editor deleting their own saved search is allowed (the global
		// no-non-admin-deletes would otherwise block it).
		if allow, deniedBy := e.EvaluateABAC("saved-searches", "delete", map[string]interface{}{"role": "editor"}); !allow {
			t.Errorf("editor delete saved-search should be allowed, denied by %q", deniedBy)
		}
		// The carve-out must NOT leak: a viewer still can't delete an incident.
		if allow, _ := e.EvaluateABAC("incidents", "delete", map[string]interface{}{"role": "viewer"}); allow {
			t.Error("viewer deleting an incident must still be denied by the global policies")
		}
	})
}

// TestHasPermission_SavedSearchesViewer verifies the RBAC half of migration 015:
// the viewer role, once granted saved-searches:write, passes the coarse write
// check for that resource but not for others.
func TestHasPermission_SavedSearchesViewer(t *testing.T) {
	e := newTestEngine(map[string]Role{
		"viewer": {Name: "viewer", Permissions: []string{"read", "saved-searches:read", "saved-searches:write"}},
	}, nil)

	if !e.HasPermission("viewer", "saved-searches", "write") {
		t.Error("viewer should have saved-searches:write after the carve-out")
	}
	if e.HasPermission("viewer", "incidents", "write") {
		t.Error("viewer must not gain write on other resources from the carve-out")
	}
}

func TestValidateCondition(t *testing.T) {
	valid := []string{
		`subject.role == "viewer"`,
		`subject.role == "viewer" && action != "read"`,
		`subject.tier == "enterprise" || subject.tenant_id == "acme"`,
		`resource.type == "incidents" && action == "delete"`,
	}
	for _, c := range valid {
		if err := ValidateCondition(c); err != nil {
			t.Errorf("expected %q to be valid, got: %v", c, err)
		}
	}

	invalid := []string{
		`subject.role ==`,            // syntax error
		`subject.role = "viewer"`,    // assignment, not comparison
		`"just a string"`,            // not boolean-typed
		`subject.role && action`,     // non-boolean operands
		`(subject.role == "viewer"`,  // unbalanced parens
	}
	for _, c := range invalid {
		if err := ValidateCondition(c); err == nil {
			t.Errorf("expected %q to be rejected, but it compiled", c)
		}
	}
}
