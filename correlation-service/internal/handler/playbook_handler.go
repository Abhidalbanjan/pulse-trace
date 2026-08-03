package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pulsetrace/correlation-service/internal/engine"
	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/remediation"
)

// PlaybookHandler is the human half of human-in-the-loop remediation: the
// endpoints an engineer uses to see what a proposed fix would do, then approve
// or decline it.
//
// Without these, the manual-approval policy would be a way to stop
// remediation, not a way to govern it — a playbook would sit in
// PENDING_APPROVAL with nothing able to move it.
type PlaybookHandler struct {
	repo   *repository.IncidentRepository
	router *engine.AutomationRouter
	// authz gates approval by the action's risk tier: the gateway RBAC lets a
	// role reach these endpoints at all, but high-risk actions (scale, rollback,
	// delete…) additionally require an elevated role. The action type is only
	// known here (on the playbook), which is why this check can't live at the
	// gateway.
	authz *remediation.ApproverAuthorizer
}

func NewPlaybookHandler(repo *repository.IncidentRepository, router *engine.AutomationRouter, authz *remediation.ApproverAuthorizer) *PlaybookHandler {
	return &PlaybookHandler{repo: repo, router: router, authz: authz}
}

func (h *PlaybookHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/incidents/{id}/playbook/approve", h.Approve)
	mux.HandleFunc("POST /api/v1/incidents/{id}/playbook/reject", h.Reject)
	mux.HandleFunc("POST /api/v1/incidents/{id}/playbook/dry-run", h.DryRun)
	mux.HandleFunc("GET /api/v1/remediation/policy", h.GetPolicy)
}

type playbookDecisionRequest struct {
	Reason string `json:"reason"`
}

// GetPolicy reports the current remediation posture.
//
// The UI needs this to avoid offering an "Approve & run" button in a
// deployment where execution is switched off — a control that silently does
// nothing is worse than an absent one.
func (h *PlaybookHandler) GetPolicy(w http.ResponseWriter, _ *http.Request) {
	policy := h.router.Policy()
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":                 string(policy.Mode),
		"confidence_threshold": policy.ConfidenceThreshold,
		"execution_allowed":    policy.AllowsExecution(),
	})
}

// Approve authorizes a pending remediation and executes it.
//
//	POST /api/v1/incidents/{id}/playbook/approve
func (h *PlaybookHandler) Approve(w http.ResponseWriter, r *http.Request) {
	inc, ok := h.loadPendingIncident(w, r)
	if !ok {
		return
	}

	// The approver's identity comes from the gateway-verified X-User-Subject
	// header, never from the request body. The gateway strips client-supplied
	// copies of these headers (see gateway auth middleware), so this cannot be
	// spoofed by the caller — and an approval attributed to whoever the client
	// claimed to be would be worthless as an audit record.
	approver := userOf(r)
	if approver == "" {
		http.Error(w, "cannot attribute approval: no authenticated user", http.StatusUnauthorized)
		return
	}

	// Per-action authorization: classify the playbook's blast radius and require
	// an elevated role for high-risk actions. The gateway already confirmed this
	// caller may reach the endpoint; this is the finer, action-aware gate on top.
	pb := inc.Causal.Playbook
	tier := remediation.ClassifyRisk(pb.Name, pb.Description)
	if ok, reason := h.authz.CanApprove(roleOf(r), tier); !ok {
		http.Error(w, "not authorized to approve this remediation: "+reason, http.StatusForbidden)
		return
	}

	err := h.router.Approve(r.Context(), inc.ID, inc.Causal, primaryService(inc), approver)
	if err != nil {
		h.writeDecisionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"playbook": inc.Causal.Playbook})
}

// Reject declines a pending remediation.
//
//	POST /api/v1/incidents/{id}/playbook/reject
func (h *PlaybookHandler) Reject(w http.ResponseWriter, r *http.Request) {
	inc, ok := h.loadPendingIncident(w, r)
	if !ok {
		return
	}

	rejecter := userOf(r)
	if rejecter == "" {
		http.Error(w, "cannot attribute rejection: no authenticated user", http.StatusUnauthorized)
		return
	}

	var body playbookDecisionRequest
	// A missing or malformed body is fine — the reason is optional.
	_ = json.NewDecoder(r.Body).Decode(&body)

	if err := h.router.Reject(r.Context(), inc.ID, inc.Causal, rejecter, body.Reason); err != nil {
		h.writeDecisionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"playbook": inc.Causal.Playbook})
}

// DryRun computes what the proposed playbook would do, without doing it.
//
// This is allowed regardless of policy mode, because it never executes — it's
// the "show me first" that makes an approval decision an informed one.
//
//	POST /api/v1/incidents/{id}/playbook/dry-run
func (h *PlaybookHandler) DryRun(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantOf(r)
	if tenantID == "" {
		http.Error(w, "missing tenant", http.StatusUnauthorized)
		return
	}

	inc, err := h.repo.GetByIDForTenant(r.Context(), tenantID, r.PathValue("id"))
	if err != nil {
		http.Error(w, "incident not found", http.StatusNotFound)
		return
	}
	if inc.Causal == nil || inc.Causal.Playbook == nil {
		http.Error(w, "incident has no suggested playbook", http.StatusNotFound)
		return
	}

	if err := h.router.DryRun(r.Context(), inc.ID, inc.Causal, primaryService(inc)); err != nil {
		h.writeDecisionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"playbook": inc.Causal.Playbook})
}

// loadPendingIncident resolves the tenant-scoped incident and confirms it has
// a playbook to act on.
func (h *PlaybookHandler) loadPendingIncident(w http.ResponseWriter, r *http.Request) (*models.Incident, bool) {
	tenantID := tenantOf(r)
	if tenantID == "" {
		http.Error(w, "missing tenant", http.StatusUnauthorized)
		return nil, false
	}

	inc, err := h.repo.GetByIDForTenant(r.Context(), tenantID, r.PathValue("id"))
	if err != nil {
		// Deliberately the same 404 a nonexistent incident gets: distinguishing
		// "not yours" from "doesn't exist" would confirm the existence of other
		// tenants' incidents to anyone probing IDs.
		http.Error(w, "incident not found", http.StatusNotFound)
		return nil, false
	}
	if inc.Causal == nil || inc.Causal.Playbook == nil {
		http.Error(w, "incident has no suggested playbook", http.StatusNotFound)
		return nil, false
	}
	return inc, true
}

func (h *PlaybookHandler) writeDecisionError(w http.ResponseWriter, err error) {
	if errors.Is(err, engine.ErrNotAwaitingApproval) {
		// 409: the playbook is in a state where this decision doesn't apply
		// (already executed, already rejected, never proposed). Retrying won't
		// help, and re-running a remediation because a button was
		// double-clicked is exactly the failure to avoid.
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Error(w, err.Error(), http.StatusForbidden)
}

// tenantOf returns the gateway-verified tenant for the request.
func tenantOf(r *http.Request) string {
	return r.Header.Get("X-Tenant-ID")
}

// userOf returns the gateway-verified subject (the authenticated user) for the
// request.
func userOf(r *http.Request) string {
	return r.Header.Get("X-User-Subject")
}

// roleOf returns the gateway-verified role for the request (set by the gateway
// auth middleware from the JWT; the gateway strips any client-supplied copy).
func roleOf(r *http.Request) string {
	return r.Header.Get("X-User-Role")
}

// primaryService picks the service a playbook should act on. Incidents can
// span several services; the causal chain is ordered upstream → downstream, so
// the head of the chain is the suspected cause and therefore the thing to
// remediate. Falling back to the first linked service keeps rule-based
// analyses (which may have no chain) actionable.
func primaryService(inc *models.Incident) string {
	if inc.Causal != nil && len(inc.Causal.Chain) > 0 {
		if from := inc.Causal.Chain[0].FromService; from != "" {
			return from
		}
	}
	if len(inc.ServiceNames) > 0 {
		return inc.ServiceNames[0]
	}
	return ""
}
