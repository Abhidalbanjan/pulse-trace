package main

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/pulse-trace/action-service/internal/handler"
	"github.com/pulse-trace/action-service/internal/k8s"
)

func main() {
	r := gin.Default()

	// Initialize Kubernetes Operator stub
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
