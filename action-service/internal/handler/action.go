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
	ActionType string            `json:"action_type"`
	Target     string            `json:"target"`
	// Namespace the target deployment lives in. Empty falls back to the operator's
	// configured default namespace, so single-namespace callers need not set it.
	Namespace  string            `json:"namespace"`
	Parameters map[string]string `json:"parameters"`
}

func (h *ActionHandler) ExecuteAction(c *gin.Context) {
	var req ExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	err := h.operator.ExecuteRunbook(req.ActionType, req.Target, req.Namespace, req.Parameters)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Action executed successfully by the PulseTrace Operator",
	})
}

func (h *ActionHandler) GetActionStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "UP"})
}
