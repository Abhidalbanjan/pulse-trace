package auth

import (
	"encoding/json"
	"testing"
)

func TestParseUserNameFilter(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		wantOK   bool
	}{
		{`userName eq "alice@example.com"`, "alice@example.com", true},
		{`USERNAME EQ "bob"`, "bob", true}, // case-insensitive attr/op
		{`displayName eq "x"`, "", false},  // unsupported attribute
		{``, "", false},
		{`userName co "partial"`, "", false}, // unsupported operator
	}
	for _, c := range cases {
		got, ok := parseUserNameFilter(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("parseUserNameFilter(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

type patchOp = struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func TestActiveFromPatch(t *testing.T) {
	// Okta-style: replace active with a path.
	active, changed := activeFromPatch([]patchOp{{Op: "replace", Path: "active", Value: json.RawMessage(`false`)}})
	if !changed || active {
		t.Errorf("expected active=false changed=true, got %v %v", active, changed)
	}
	// Azure-style: pathless replace carrying {active:false}.
	active, changed = activeFromPatch([]patchOp{{Op: "Replace", Path: "", Value: json.RawMessage(`{"active":false}`)}})
	if !changed || active {
		t.Errorf("expected pathless deprovision to be detected, got %v %v", active, changed)
	}
	// Reactivate.
	active, changed = activeFromPatch([]patchOp{{Op: "replace", Path: "active", Value: json.RawMessage(`true`)}})
	if !changed || !active {
		t.Errorf("expected active=true changed=true, got %v %v", active, changed)
	}
	// Unrelated op → no change.
	_, changed = activeFromPatch([]patchOp{{Op: "replace", Path: "displayName", Value: json.RawMessage(`"x"`)}})
	if changed {
		t.Error("unrelated patch op must not toggle active")
	}
}

func TestSCIMUser_Render(t *testing.T) {
	u := dbUser{id: "abc", username: "alice", active: true}
	su := u.toSCIM()
	if su.UserName != "alice" || !su.Active || su.Meta.Location != "/scim/v2/Users/abc" {
		t.Fatalf("unexpected SCIM rendering: %+v", su)
	}
	if len(su.Schemas) == 0 || su.Schemas[0] != scimUserSchema {
		t.Fatal("SCIM user must carry the core User schema")
	}
}
