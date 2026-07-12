package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"

	"github.com/pulsetrace/correlation-service/internal/llm"
	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
)

// SLOHandler exposes HTTP endpoints for managing SLO definitions
// and querying the SLO dashboard data.
type SLOHandler struct {
	repo *repository.SLORepository
	llm  llm.Provider
}

func NewSLOHandler(repo *repository.SLORepository, llmProvider llm.Provider) *SLOHandler {
	return &SLOHandler{repo: repo, llm: llmProvider}
}

// RegisterRoutes wires up all SLO-related routes.
func (h *SLOHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/slo/definitions", h.ListDefinitions)
	mux.HandleFunc("POST /api/v1/slo/definitions", h.CreateDefinition)
	mux.HandleFunc("DELETE /api/v1/slo/definitions/{id}", h.DeleteDefinition)
	mux.HandleFunc("GET /api/v1/slo/dashboard", h.Dashboard)
	mux.HandleFunc("GET /api/v1/slo/budget-alerts", h.ListBudgetAlerts)
	mux.HandleFunc("POST /api/v1/slo/evaluate-pr", h.EvaluatePR)
}

type PREvaluationRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (h *SLOHandler) EvaluatePR(w http.ResponseWriter, r *http.Request) {
	var req PREvaluationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.Fail("invalid request body"))
		return
	}

	prompt := fmt.Sprintf(`You are an SRE AI evaluator. Evaluate if the following GitHub Pull Request might cause a severe SLO violation or outage based on its title and body.
If the PR explicitly introduces breaking database schema changes without backwards compatibility, hardcodes dangerous credentials, or removes critical rate limiting logic, you should block it.
Otherwise, approve it.

PR Title: %s
PR Body: %s

Respond with ONLY the word "BLOCK" if it violates SLOs, or "APPROVE" if it looks safe.`, req.Title, req.Body)

	messages := []llm.Message{{Role: "user", Content: prompt}}
	resp, err := h.llm.Chat(r.Context(), messages)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("LLM evaluation failed"))
		return
	}

	respStr := strings.TrimSpace(strings.ToUpper(resp.Text))
	
	result := map[string]string{"decision": "APPROVE"}
	if strings.Contains(respStr, "BLOCK") {
		result["decision"] = "BLOCK"
	}
	
	writeJSON(w, http.StatusOK, models.OK(result))
}

// ListDefinitions returns all configured SLO definitions.
//
//	GET /api/v1/slo/definitions
func (h *SLOHandler) ListDefinitions(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "slo.list_definitions")
	defer span.End()

	defs, err := h.repo.ListDefinitions(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to list definitions: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(defs))
}

// CreateDefinition creates or updates an SLO definition.
//
//	POST /api/v1/slo/definitions
func (h *SLOHandler) CreateDefinition(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "slo.create_definition")
	defer span.End()

	var req models.CreateSLORequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.Fail("invalid request body"))
		return
	}

	if req.ServiceName == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("service_name is required"))
		return
	}
	if req.SLOTarget <= 0 || req.SLOTarget > 100 {
		writeJSON(w, http.StatusBadRequest, models.Fail("slo_target must be between 0 and 100"))
		return
	}
	if req.SLIType == "" {
		req.SLIType = "availability"
	}
	if req.WindowDays <= 0 {
		req.WindowDays = 30
	}

	def := &models.SLODefinition{
		ID:          uuid.New().String(),
		ServiceName: req.ServiceName,
		SLOTarget:   req.SLOTarget,
		SLIType:     req.SLIType,
		WindowDays:  req.WindowDays,
	}

	result, err := h.repo.UpsertDefinition(ctx, def)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to create definition: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusCreated, models.OK(result))
}

// DeleteDefinition removes an SLO definition by ID.
//
//	DELETE /api/v1/slo/definitions/{id}
func (h *SLOHandler) DeleteDefinition(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "slo.delete_definition")
	defer span.End()

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.Fail("id is required"))
		return
	}

	if err := h.repo.DeleteDefinition(ctx, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to delete definition: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, models.OK(map[string]string{"deleted": id}))
}

// Dashboard returns the full SLO dashboard data for all configured services.
//
//	GET /api/v1/slo/dashboard
func (h *SLOHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "slo.dashboard")
	defer span.End()

	defs, err := h.repo.ListDefinitions(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail("failed to list definitions: "+err.Error()))
		return
	}

	now := time.Now().UTC()
	var items []models.SLODashboardItem

	for _, def := range defs {
		windowStart := now.AddDate(0, 0, -def.WindowDays)

		// Compute current SLI
		total, errors, sli, err := h.repo.ComputeSLI(ctx, def.ServiceName, windowStart, now)
		if err != nil {
			sli = 100.0
		}

		// Error budget calculations
		errorBudgetTotal := 100.0 - def.SLOTarget            // e.g. 0.1% for 99.9%
		errorBudgetUsed := 100.0 - sli                       // current error percentage
		budgetRemainingPct := 100.0
		if errorBudgetTotal > 0 {
			budgetRemainingPct = ((errorBudgetTotal - errorBudgetUsed) / errorBudgetTotal) * 100.0
			if budgetRemainingPct < 0 {
				budgetRemainingPct = 0
			}
			if budgetRemainingPct > 100 {
				budgetRemainingPct = 100
			}
		}

		// Budget in minutes
		totalMinutes := float64(def.WindowDays) * 24.0 * 60.0
		budgetTotalMin := (errorBudgetTotal / 100.0) * totalMinutes
		budgetUsedMin := (errorBudgetUsed / 100.0) * totalMinutes
		if budgetUsedMin < 0 {
			budgetUsedMin = 0
		}

		// Burn rate: how fast is the budget being consumed?
		burnRate := 0.0
		if errorBudgetTotal > 0 {
			burnRate = errorBudgetUsed / errorBudgetTotal
		}

		// Status determination
		status := "healthy"
		if budgetRemainingPct < 10 {
			status = "critical"
		} else if budgetRemainingPct < 50 {
			status = "warning"
		}

		// Get trend data (7 days)
		trend, _ := h.repo.GetTrend(ctx, def.ServiceName, 7)
		if trend == nil {
			trend = []models.SLOTrendPoint{}
		}

		item := models.SLODashboardItem{
			Definition:         *def,
			CurrentSLI:         sli,
			TotalEvents:        total,
			ErrorEvents:        errors,
			BudgetTotalMin:     budgetTotalMin,
			BudgetUsedMin:      budgetUsedMin,
			BudgetRemainingPct: budgetRemainingPct,
			BurnRate:           burnRate,
			Status:             status,
			Trend:              trend,
		}
		items = append(items, item)
	}

	if items == nil {
		items = []models.SLODashboardItem{}
	}

	writeJSON(w, http.StatusOK, models.OK(items))
}

// ListBudgetAlerts returns recent burn rate breach events.
//
//	GET /api/v1/slo/budget-alerts?service=payment-service&limit=20
func (h *SLOHandler) ListBudgetAlerts(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(r.Context(), "slo.list_budget_alerts")
	defer span.End()

	service := r.URL.Query().Get("service")
	alerts, err := h.repo.ListBudgetAlerts(ctx, service, 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.Fail(
			fmt.Sprintf("failed to list budget alerts: %v", err),
		))
		return
	}
	if alerts == nil {
		alerts = []*models.SLOBudgetAlert{}
	}
	writeJSON(w, http.StatusOK, models.OK(alerts))
}
