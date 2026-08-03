package remediation

import "testing"

func TestClassifyRisk(t *testing.T) {
	cases := []struct {
		name, desc string
		want       RiskTier
	}{
		// Low-risk: restarts / recycles.
		{"restart_service", "rolling restart of the pods", RiskLow},
		{"recycle_db_pool", "recycle stale connections", RiskLow},
		{"bounce", "", RiskLow},
		// High-risk: capacity/version/state changes.
		{"scale_replicas", "scale up by 2", RiskHigh},
		{"Execute Rollback", "roll back to previous version", RiskHigh},
		{"drain_node", "cordon and drain", RiskHigh},
		{"delete_pod", "terminate the pod", RiskHigh},
		{"failover_db", "fail over to replica", RiskHigh},
		// Unknown → high (fail safe).
		{"frobnicate", "do something novel", RiskHigh},
		{"", "", RiskHigh},
	}
	for _, c := range cases {
		if got := ClassifyRisk(c.name, c.desc); got != c.want {
			t.Errorf("ClassifyRisk(%q,%q) = %q, want %q", c.name, c.desc, got, c.want)
		}
	}
}

func TestApproverAuthorizer(t *testing.T) {
	a := NewApproverAuthorizer(ParseRoles("sre, oncall"))

	// admin is always elevated (implicitly added).
	if !a.IsElevated("admin") {
		t.Error("admin must be elevated")
	}
	if !a.IsElevated("SRE") { // case-insensitive
		t.Error("configured role sre must be elevated (case-insensitive)")
	}
	if a.IsElevated("editor") {
		t.Error("editor is not in the elevated set")
	}

	// Low-risk: any authenticated role may approve.
	if ok, _ := a.CanApprove("editor", RiskLow); !ok {
		t.Error("editor should approve low-risk actions")
	}
	// High-risk: only elevated roles.
	if ok, _ := a.CanApprove("editor", RiskHigh); ok {
		t.Error("editor must NOT approve high-risk actions")
	}
	if ok, _ := a.CanApprove("admin", RiskHigh); !ok {
		t.Error("admin must approve high-risk actions")
	}
	if ok, _ := a.CanApprove("oncall", RiskHigh); !ok {
		t.Error("configured elevated role oncall must approve high-risk actions")
	}
	// No role at all is always denied.
	if ok, _ := a.CanApprove("", RiskLow); ok {
		t.Error("an empty role must never approve")
	}
}

func TestApproverAuthorizer_DefaultOnlyAdmin(t *testing.T) {
	a := NewApproverAuthorizer(nil)
	if ok, _ := a.CanApprove("sre", RiskHigh); ok {
		t.Error("with no configured elevated roles, only admin approves high-risk")
	}
	if ok, _ := a.CanApprove("admin", RiskHigh); !ok {
		t.Error("admin approves high-risk by default")
	}
}
