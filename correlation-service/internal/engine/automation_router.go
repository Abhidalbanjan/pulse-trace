package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/remediation"
)

// ErrNotAwaitingApproval is returned when an approve/reject call targets a
// playbook that isn't actually waiting on a human — an already-executed
// playbook, or one the policy never proposed. Callers surface this as a 409
// rather than silently re-running a remediation.
var ErrNotAwaitingApproval = errors.New("playbook is not awaiting approval")

// PlaybookRepository abstracts database updates for incident causal analysis.
type PlaybookRepository interface {
	UpdateCausalAnalysis(ctx context.Context, incidentID string, c *models.CausalAnalysis) error
}

// AutomationRouter decides what happens to a recovery playbook suggested by
// causal AI, and executes it when — and only when — the remediation policy
// allows.
//
// Before the policy existed this type had one behaviour: confidence ≥ 0.70
// meant "run it against production now". The confidence score is an LLM's
// self-assessment, not an authorization decision, so that is now the opt-in
// ModeAuto rather than the only option. See shared/remediation.
type AutomationRouter struct {
	repo         PlaybookRepository
	agentURL     string
	sharedSecret []byte
	httpClient   *http.Client
	policy       remediation.Policy
}

func NewAutomationRouter(repo PlaybookRepository, agentURL, secret string) *AutomationRouter {
	return NewAutomationRouterWithPolicy(repo, agentURL, secret, remediation.DefaultPolicy())
}

// NewAutomationRouterWithPolicy is the constructor to use in production, where
// the policy comes from configuration. NewAutomationRouter keeps the safe
// default (human approval required) for callers that don't care.
func NewAutomationRouterWithPolicy(repo PlaybookRepository, agentURL, secret string, policy remediation.Policy) *AutomationRouter {
	if secret == "" {
		secret = "pulsetrace_secure_playbook_hmac_secret"
	}
	return &AutomationRouter{
		repo:         repo,
		agentURL:     agentURL,
		sharedSecret: []byte(secret),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		policy: policy,
	}
}

// Policy exposes the router's resolved policy, so handlers can report the
// current posture to the UI without reading env vars themselves.
func (r *AutomationRouter) Policy() remediation.Policy { return r.policy }

// Sign produces the HMAC over a playbook execution request.
//
// dryRun is inside the signed payload deliberately. The dangerous direction is
// an attacker flipping a dry run into a real one; if the flag rode outside the
// signature, anyone able to reach the agent endpoint could replay a legitimate
// dry-run request as a live production change.
func (r *AutomationRouter) Sign(incidentID, playbookName, serviceName string, timestamp time.Time, dryRun bool) string {
	payload := fmt.Sprintf("%s:%s:%s:%d:dry_run=%t", incidentID, playbookName, serviceName, timestamp.Unix(), dryRun)
	mac := hmac.New(sha256.New, r.sharedSecret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// Route applies the remediation policy to a freshly-analyzed incident.
//
// It never returns an error: routing is a side effect of incident correlation,
// and a failure to remediate must not fail the correlation itself. Outcomes
// are recorded on the incident's playbook status instead.
func (r *AutomationRouter) Route(ctx context.Context, incidentID string, causal *models.CausalAnalysis, targetService string) {
	if causal == nil || causal.Playbook == nil {
		return
	}

	playbook := causal.Playbook
	decision := r.policy.Decide(causal.Confidence)

	switch decision {
	case remediation.DecisionSuppressed:
		log.Printf("AUTOMATION ROUTER: remediation is off; playbook %q for %q recorded but not proposed.",
			playbook.Name, targetService)
		r.persist(ctx, incidentID, causal, models.PlaybookSuppressed)

	case remediation.DecisionSuggested:
		log.Printf("AUTOMATION ROUTER: confidence %.2f is below the %.2f threshold. Playbook %q is suggested but not proposed for execution.",
			causal.Confidence, r.policy.ConfidenceThreshold, playbook.Name)
		r.persist(ctx, incidentID, causal, models.PlaybookSuggested)

	case remediation.DecisionPendingApproval:
		log.Printf("AUTOMATION ROUTER: confidence %.2f clears the threshold, but policy is %q — playbook %q for %q awaits human approval.",
			causal.Confidence, r.policy.Mode, playbook.Name, targetService)
		r.persist(ctx, incidentID, causal, models.PlaybookPendingApproval)

	case remediation.DecisionDryRun:
		log.Printf("AUTOMATION ROUTER: policy is dry-run — planning playbook %q for %q without changing anything.",
			playbook.Name, targetService)
		playbook.DryRun = true
		r.persist(ctx, incidentID, causal, models.PlaybookExecuting)
		r.dispatch(ctx, incidentID, causal, targetService, true)

	case remediation.DecisionExecute:
		log.Printf("AUTOMATION ROUTER: policy is auto and confidence %.2f clears the threshold — executing playbook %q for %q.",
			causal.Confidence, playbook.Name, targetService)
		playbook.DryRun = false
		r.persist(ctx, incidentID, causal, models.PlaybookExecuting)
		r.dispatch(ctx, incidentID, causal, targetService, false)
	}
}

// Approve executes a playbook that was held for human approval.
//
// The approver's identity is recorded on the playbook before execution starts,
// so the audit trail survives a crash mid-remediation: "who authorized this"
// must be answerable even when the answer to "did it finish" is no.
func (r *AutomationRouter) Approve(ctx context.Context, incidentID string, causal *models.CausalAnalysis, targetService, approvedBy string) error {
	if causal == nil || causal.Playbook == nil {
		return ErrNotAwaitingApproval
	}
	if causal.Playbook.Status != models.PlaybookPendingApproval {
		return fmt.Errorf("%w: status is %q", ErrNotAwaitingApproval, causal.Playbook.Status)
	}
	if !r.policy.AllowsExecution() {
		// The policy changed to off/dry-run after this playbook was proposed.
		// An approval recorded under the old posture doesn't override the
		// current one.
		return fmt.Errorf("remediation policy %q does not permit execution", r.policy.Mode)
	}

	now := time.Now().UTC()
	causal.Playbook.ApprovedBy = approvedBy
	causal.Playbook.ApprovedAt = &now
	causal.Playbook.DryRun = false

	log.Printf("AUTOMATION ROUTER: playbook %q for %q approved by %q — executing.",
		causal.Playbook.Name, targetService, approvedBy)

	r.persist(ctx, incidentID, causal, models.PlaybookExecuting)
	r.dispatch(ctx, incidentID, causal, targetService, false)
	return nil
}

// Reject records that a human declined the proposed remediation.
func (r *AutomationRouter) Reject(ctx context.Context, incidentID string, causal *models.CausalAnalysis, rejectedBy, reason string) error {
	if causal == nil || causal.Playbook == nil {
		return ErrNotAwaitingApproval
	}
	if causal.Playbook.Status != models.PlaybookPendingApproval {
		return fmt.Errorf("%w: status is %q", ErrNotAwaitingApproval, causal.Playbook.Status)
	}

	now := time.Now().UTC()
	causal.Playbook.RejectedBy = rejectedBy
	causal.Playbook.RejectedAt = &now
	if reason != "" {
		causal.Playbook.Output = "Rejected: " + reason
	}

	log.Printf("AUTOMATION ROUTER: playbook %q rejected by %q.", causal.Playbook.Name, rejectedBy)
	r.persist(ctx, incidentID, causal, models.PlaybookRejected)
	return nil
}

// DryRun plans a proposed playbook without changing anything, on demand — the
// "show me what this would do" button next to an approval request. It does not
// require the policy to allow execution, because it never executes.
func (r *AutomationRouter) DryRun(ctx context.Context, incidentID string, causal *models.CausalAnalysis, targetService string) error {
	if causal == nil || causal.Playbook == nil {
		return ErrNotAwaitingApproval
	}
	causal.Playbook.DryRun = true
	r.dispatch(ctx, incidentID, causal, targetService, true)
	return nil
}

// dispatch posts a signed execution request to the remediation agent and
// records the outcome on the incident.
func (r *AutomationRouter) dispatch(ctx context.Context, incidentID string, causal *models.CausalAnalysis, targetService string, dryRun bool) {
	playbook := causal.Playbook

	ts := time.Now().UTC()
	reqPayload := map[string]any{
		"incident_id":   incidentID,
		"playbook_name": playbook.Name,
		"service_name":  targetService,
		"timestamp":     ts.Format(time.RFC3339),
		"dry_run":       dryRun,
		"signature":     r.Sign(incidentID, playbook.Name, targetService, ts, dryRun),
	}

	b, err := json.Marshal(reqPayload)
	if err != nil {
		log.Printf("AUTOMATION ROUTER: failed to marshal payload: %v", err)
		r.updateStatus(ctx, incidentID, causal, models.PlaybookFailed, "Failed to marshal request payload: "+err.Error())
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.agentURL, bytes.NewReader(b))
	if err != nil {
		log.Printf("AUTOMATION ROUTER: failed to create HTTP request: %v", err)
		r.updateStatus(ctx, incidentID, causal, models.PlaybookFailed, "Failed to create HTTP request: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		log.Printf("AUTOMATION ROUTER: secure agent execution failed: %v", err)
		r.updateStatus(ctx, incidentID, causal, models.PlaybookFailed, "Secure agent connection failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("AUTOMATION ROUTER: secure agent returned status %d", resp.StatusCode)
		r.updateStatus(ctx, incidentID, causal, models.PlaybookFailed, fmt.Sprintf("Secure agent returned status %d", resp.StatusCode))
		return
	}

	var agentResp struct {
		Status string `json:"status"`
		Output string `json:"output"`
		DryRun bool   `json:"dry_run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agentResp); err != nil {
		log.Printf("AUTOMATION ROUTER: failed to decode agent response: %v", err)
		r.updateStatus(ctx, incidentID, causal, models.PlaybookFailed, "Failed to decode agent response: "+err.Error())
		return
	}

	// Trust the agent's own account of whether it changed anything over what
	// we asked for. An on-prem agent configured for dry-run will refuse a live
	// request; recording that as a completed remediation would be a lie in the
	// incident timeline.
	playbook.DryRun = agentResp.DryRun

	log.Printf("AUTOMATION ROUTER: playbook %q result: status=%s, dry_run=%t, output=%q",
		playbook.Name, agentResp.Status, agentResp.DryRun, agentResp.Output)
	r.updateStatus(ctx, incidentID, causal, agentResp.Status, agentResp.Output)
}

func (r *AutomationRouter) persist(ctx context.Context, incidentID string, causal *models.CausalAnalysis, status string) {
	causal.Playbook.Status = status
	if err := r.repo.UpdateCausalAnalysis(ctx, incidentID, causal); err != nil {
		log.Printf("AUTOMATION ROUTER: failed to update playbook status to %s: %v", status, err)
	}
}

func (r *AutomationRouter) updateStatus(ctx context.Context, incidentID string, causal *models.CausalAnalysis, status, output string) {
	causal.Playbook.Output = output
	r.persist(ctx, incidentID, causal, status)
}
