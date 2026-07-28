package k8s

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// The remediation policy vocabulary is defined canonically in
// shared/remediation. It is re-implemented here, deliberately, because
// action-service is the one service in the repo with no dependency on the
// shared module — its Dockerfile copies only its own module (see the comment
// there), and pulling in shared would drag Kafka, Postgres and OTel clients
// into a service that needs none of them.
//
// Only the subset action-service actually uses is duplicated: the four mode
// names and the "may I mutate the cluster?" question. The confidence
// threshold is a control-plane concern and has no meaning here — by the time
// a request reaches this service, the decision to act has already been made
// elsewhere; this policy is the local veto.
//
// If the mode names change in shared/remediation, they must change here too.

// Mode is the operator-configured remediation posture.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeDryRun Mode = "dry-run"
	ModeManual Mode = "manual"
	ModeAuto   Mode = "auto"
)

// Policy is action-service's local remediation posture.
type Policy struct {
	Mode Mode
}

// AllowsExecution reports whether this operator may mutate the cluster at all.
// Off and dry-run may not; manual and auto may, because by the time a request
// arrives here the human gate (if any) has already been passed upstream.
func (p Policy) AllowsExecution() bool {
	return p.Mode == ModeManual || p.Mode == ModeAuto
}

// ParseMode maps a configuration string onto a Mode, accepting the same
// spellings shared/remediation does.
//
// An unrecognized value is an error, not a fallback: if an operator writes
// REMEDIATION_MODE=dryrun and silently gets execution, the failure mode is a
// production outage.
func ParseMode(s string) (Mode, error) {
	normalized := strings.ToLower(strings.TrimSpace(s))
	normalized = strings.NewReplacer("_", "-", " ", "-").Replace(normalized)

	switch normalized {
	case string(ModeOff), "disabled", "none":
		return ModeOff, nil
	case string(ModeDryRun), "dry", "plan":
		return ModeDryRun, nil
	case string(ModeManual), "approval", "approve":
		return ModeManual, nil
	case string(ModeAuto), "automatic":
		return ModeAuto, nil
	default:
		return "", fmt.Errorf("unknown remediation mode %q (want one of: off, dry-run, manual, auto)", s)
	}
}

// policyFromEnv reads REMEDIATION_MODE, defaulting to the safe posture
// (manual — execution permitted only because approval happens upstream, but
// never auto-proposed here) and logging anything it can't parse.
func policyFromEnv() Policy {
	policy := Policy{Mode: ModeManual}

	raw := os.Getenv("REMEDIATION_MODE")
	if strings.TrimSpace(raw) == "" {
		return policy
	}

	mode, err := ParseMode(raw)
	if err != nil {
		log.Printf("[K8s Operator] WARNING: %v — using remediation policy %q", err, policy.Mode)
		return policy
	}
	policy.Mode = mode
	return policy
}
