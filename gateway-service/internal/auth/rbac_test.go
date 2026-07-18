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
}
