package main

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/pulse-trace/action-service/internal/handler"
	"github.com/pulse-trace/action-service/internal/k8s"
)

func main() {
	r := gin.Default()

	// Initialize the Kubernetes Operator. It runs in MOCK mode (logs actions
	// instead of calling the cluster) only when no in-cluster or ~/.kube/config
	// credentials are found — see k8s.NewOperator. RESTART_PODS, SCALE, and
	// ROLLBACK are real client-go calls when a real cluster is reachable.
	operator := k8s.NewOperator()
	actionHandler := handler.NewActionHandler(operator)

	api := r.Group("/api/v1")
	{
		api.POST("/actions/execute", actionHandler.ExecuteAction)
		api.GET("/actions/status", actionHandler.GetActionStatus)
	}

	log.Println("Action Service (Auto-Remediation Engine) starting on :8085")
	if err := r.Run(":8085"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
