package remediation

import "strings"

// RiskTier classifies how much blast radius a remediation action carries, so
// approval can require a stronger role for the dangerous ones. The remediation
// policy (Mode) governs *whether* a human is asked; this governs *who* may say
// yes to a given action.
type RiskTier string

const (
	// RiskLow: reversible, self-healing-style actions — a rolling restart or a
	// connection-pool recycle. Any user already permitted to act on incidents can
	// approve these.
	RiskLow RiskTier = "low"

	// RiskHigh: actions that change capacity, version, or state and are harder to
	// undo — scaling, rollbacks, deletes, drains, failovers. Approving these
	// requires an elevated role. Unknown/unrecognized actions are treated as high
	// (fail safe: an action we can't classify is not one to wave through).
	RiskHigh RiskTier = "high"
)

// highRiskKeywords mark actions whose blast radius warrants an elevated approver.
// Matched case-insensitively against the playbook name + description.
var highRiskKeywords = []string{
	"scale", "rollback", "roll back", "revert",
	"delete", "destroy", "remove", "terminate",
	"drain", "cordon", "evict", "failover", "fail over",
	"redeploy", "rollout undo",
}

// lowRiskKeywords mark actions we recognize as low blast-radius. Anything that
// matches neither list is treated as high risk.
var lowRiskKeywords = []string{
	"restart", "recycle", "reboot", "bounce", "refresh", "clear cache",
}

// ClassifyRisk buckets a remediation action (identified by its name and, as a
// fallback, its human description) into a RiskTier. It's keyword-based because the
// action label is free-form: the rule-based analyzer emits names like
// "restart_service"/"scale_replicas", and the LLM analyzer emits arbitrary titles
// and a type such as "ROLLBACK".
func ClassifyRisk(name, description string) RiskTier {
	hay := strings.ToLower(name + " " + description)
	for _, kw := range highRiskKeywords {
		if strings.Contains(hay, kw) {
			return RiskHigh
		}
	}
	for _, kw := range lowRiskKeywords {
		if strings.Contains(hay, kw) {
			return RiskLow
		}
	}
	// Unrecognized → treat as high risk so a novel action can't be approved by a
	// low-privilege role just because we didn't have a keyword for it.
	return RiskHigh
}

// ApproverAuthorizer decides whether a role may approve a remediation of a given
// risk tier. Low-risk actions are approvable by any authenticated caller the
// gateway already let through (it enforces the coarse incidents/remediation
// permission); high-risk actions additionally require an elevated role.
type ApproverAuthorizer struct {
	elevatedRoles map[string]bool
}

// NewApproverAuthorizer builds an authorizer whose elevated roles are the given
// set plus "admin" (always elevated). Pass the parsed operator config; an empty
// set means only admin may approve high-risk actions.
func NewApproverAuthorizer(elevatedRoles []string) *ApproverAuthorizer {
	set := map[string]bool{"admin": true}
	for _, r := range elevatedRoles {
		if r = strings.TrimSpace(strings.ToLower(r)); r != "" {
			set[r] = true
		}
	}
	return &ApproverAuthorizer{elevatedRoles: set}
}

// ParseRoles splits a comma-separated role list (e.g. from an env var).
func ParseRoles(csv string) []string {
	var out []string
	for _, r := range strings.Split(csv, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// IsElevated reports whether the role is permitted to approve high-risk actions.
func (a *ApproverAuthorizer) IsElevated(role string) bool {
	return a.elevatedRoles[strings.ToLower(strings.TrimSpace(role))]
}

// CanApprove reports whether role may approve an action of the given risk tier,
// with a human-readable reason when it may not.
func (a *ApproverAuthorizer) CanApprove(role string, tier RiskTier) (bool, string) {
	if strings.TrimSpace(role) == "" {
		return false, "no authenticated role"
	}
	if tier == RiskHigh && !a.IsElevated(role) {
		return false, "high-risk remediation requires an elevated role (e.g. admin); role " + role + " may only approve low-risk actions"
	}
	return true, ""
}
