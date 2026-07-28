// Package remediation defines the policy that governs whether PulseTrace is
// allowed to change a customer's running infrastructure by itself.
//
// The self-healing pipeline (correlation-service's AutomationRouter →
// topology-service's signed agent endpoint → kubectl/docker/SQL) previously
// had exactly one behaviour: if the causal analyzer's confidence cleared a
// threshold, the playbook ran. That is a hard blocker for an enterprise buyer
// — an LLM confidence score is not an authorization decision, and no security
// review approves "the observability tool restarts prod deployments on its
// own" as a non-configurable default.
//
// This package makes that behaviour a policy with four modes, shared by every
// service in the chain so they cannot disagree about what is permitted.
package remediation

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Mode is the operator-configured remediation posture.
type Mode string

const (
	// ModeOff never executes and never asks. Playbooks are recorded as
	// suggestions only. For customers who want the causal analysis but do all
	// remediation through their own tooling.
	ModeOff Mode = "off"

	// ModeDryRun plans the remediation and records exactly what would have
	// run, without mutating anything. This is the mode to roll out with: it
	// builds an audit trail of what the system *would* have done, which is
	// what earns the trust needed to enable a stronger mode later.
	ModeDryRun Mode = "dry-run"

	// ModeManual requires a human to approve each proposed remediation before
	// it executes. This is the default, because a tool that mutates
	// production by default is not one a security review will pass.
	ModeManual Mode = "manual"

	// ModeAuto executes without human involvement when confidence clears the
	// threshold. This was the previous hardcoded behaviour; it is still
	// supported, but it is now an explicit opt-in rather than an unavoidable
	// property of running the platform.
	ModeAuto Mode = "auto"
)

// DefaultConfidenceThreshold preserves the previous hardcoded 0.70 gate. It
// applies in ModeAuto (execute directly) and ModeManual (propose for
// approval); below it, a playbook is only ever a suggestion.
const DefaultConfidenceThreshold = 0.70

// ParseMode maps a configuration string onto a Mode. It accepts the
// underscore and space spellings of "dry-run" because config files disagree
// about which is idiomatic and a typo here should not silently arm
// auto-remediation.
//
// An unrecognized value is an error rather than a fallback to a default: if an
// operator writes REMEDIATION_MODE=dryrun and gets auto-execution because the
// value didn't parse, the failure mode is a production outage.
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

// Policy is the resolved remediation configuration for a service.
type Policy struct {
	Mode Mode

	// ConfidenceThreshold is the causal-analysis confidence below which a
	// playbook is never proposed for execution or approval — it stays a
	// suggestion. It does not, by itself, authorize anything: clearing the
	// threshold in ModeManual still requires a human.
	ConfidenceThreshold float64
}

// DefaultPolicy is the safe posture: propose, never act unilaterally.
func DefaultPolicy() Policy {
	return Policy{Mode: ModeManual, ConfidenceThreshold: DefaultConfidenceThreshold}
}

// PolicyFromEnv reads REMEDIATION_MODE and REMEDIATION_CONFIDENCE_THRESHOLD.
//
// Unset means DefaultPolicy (manual approval). Invalid values return an error
// with the safe default policy alongside, so a caller that chooses to keep
// running after logging the error still runs in the restrictive mode rather
// than the permissive one.
func PolicyFromEnv() (Policy, error) {
	policy := DefaultPolicy()

	if raw := os.Getenv("REMEDIATION_MODE"); strings.TrimSpace(raw) != "" {
		mode, err := ParseMode(raw)
		if err != nil {
			return policy, err
		}
		policy.Mode = mode
	}

	if raw := os.Getenv("REMEDIATION_CONFIDENCE_THRESHOLD"); strings.TrimSpace(raw) != "" {
		threshold, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return policy, fmt.Errorf("invalid REMEDIATION_CONFIDENCE_THRESHOLD %q: %w", raw, err)
		}
		if threshold < 0 || threshold > 1 {
			return policy, fmt.Errorf("REMEDIATION_CONFIDENCE_THRESHOLD %v is outside [0,1]", threshold)
		}
		policy.ConfidenceThreshold = threshold
	}

	return policy, nil
}

// Decision is what the policy says should happen to a proposed playbook.
type Decision string

const (
	// DecisionSuppressed — remediation is switched off entirely.
	DecisionSuppressed Decision = "SUPPRESSED"

	// DecisionSuggested — recorded as a suggestion; confidence was too low to
	// propose acting on it.
	DecisionSuggested Decision = "SUGGESTED"

	// DecisionDryRun — plan and record what would run; change nothing.
	DecisionDryRun Decision = "DRY_RUN"

	// DecisionPendingApproval — wait for a human to approve or reject.
	DecisionPendingApproval Decision = "PENDING_APPROVAL"

	// DecisionExecute — run it now.
	DecisionExecute Decision = "EXECUTE"
)

// Decide applies the policy to a proposed playbook's confidence score.
//
// The confidence gate is checked before the mode for every mode except Off,
// so a low-confidence hypothesis never reaches a human as an approval request
// either — approval fatigue is how human-in-the-loop controls stop being real
// controls.
func (p Policy) Decide(confidence float64) Decision {
	if p.Mode == ModeOff {
		return DecisionSuppressed
	}
	if confidence < p.ConfidenceThreshold {
		return DecisionSuggested
	}

	switch p.Mode {
	case ModeDryRun:
		return DecisionDryRun
	case ModeAuto:
		return DecisionExecute
	default:
		// ModeManual, and any mode that somehow reached here unrecognized:
		// require a human. Defaulting an unknown mode to the most permissive
		// outcome is exactly the bug this package exists to prevent.
		return DecisionPendingApproval
	}
}

// AllowsExecution reports whether the policy permits actually mutating
// infrastructure at all.
//
// Execution points (the topology-service agent endpoint, action-service's
// Kubernetes operator) call this to enforce the policy a second time, rather
// than trusting that the caller already did. The caller is a different service
// across a network boundary; an on-prem operator who sets the agent to
// dry-run means it, regardless of what the control plane asks for.
func (p Policy) AllowsExecution() bool {
	return p.Mode == ModeManual || p.Mode == ModeAuto
}
