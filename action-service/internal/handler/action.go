package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/pulse-trace/action-service/internal/k8s"
)

type ActionHandler struct {
	operator *k8s.Operator
}

func NewActionHandler(operator *k8s.Operator) *ActionHandler {
	return &ActionHandler{operator: operator}
}

type ExecuteRequest struct {
	ActionType string `json:"action_type"`
	Target     string `json:"target"`
	// Namespace the target deployment lives in. Empty falls back to the operator's
	// configured default namespace, so single-namespace callers need not set it.
	Namespace string `json:"namespace"`
	// DryRun asks for the plan without applying it. The operator also forces a
	// dry run whenever its own REMEDIATION_MODE forbids execution, so a false
	// here is a request, not a guarantee — check dry_run on the response.
	DryRun     bool              `json:"dry_run"`
	Parameters map[string]string `json:"parameters"`
}

func (h *ActionHandler) ExecuteAction(c *gin.Context) {
	var req ExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	result, err := h.operator.ExecuteRunbook(req.ActionType, req.Target, req.Namespace, req.Parameters, req.DryRun)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// The response distinguishes a plan from a change. A caller that logs
	// "Action executed successfully" for a dry run would put a false entry in
	// the incident timeline, which is exactly the trust problem this mode
	// exists to solve.
	message := "Action executed successfully by the PulseTrace Operator"
	if result.DryRun {
		message = "Dry run only — nothing was changed"
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"dry_run": result.DryRun,
		"plan":    result.Plan,
		"message": message,
	})
}

// GetActionStatus reports liveness and the operator's current remediation
// posture, so a UI can tell the user whether this agent is even permitted to
// act before offering them a button that says it will.
func (h *ActionHandler) GetActionStatus(c *gin.Context) {
	policy := h.operator.Policy()
	c.JSON(http.StatusOK, gin.H{
		"status":            "UP",
		"remediation_mode":  string(policy.Mode),
		"execution_allowed": policy.AllowsExecution(),
	})
}
