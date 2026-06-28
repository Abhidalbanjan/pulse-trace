package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/pulsetrace/shared/models"
)

// PlaybookRepository abstracts database updates for incident causal analysis.
type PlaybookRepository interface {
	UpdateCausalAnalysis(ctx context.Context, incidentID string, c *models.CausalAnalysis) error
}

// AutomationRouter handles execution of recovery playbooks suggested by causal AI.
type AutomationRouter struct {
	repo         PlaybookRepository
	agentURL     string
	sharedSecret []byte
	httpClient   *http.Client
}

func NewAutomationRouter(repo PlaybookRepository, agentURL, secret string) *AutomationRouter {
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
	}
}

func (r *AutomationRouter) Sign(incidentID, playbookName, serviceName string, timestamp time.Time) string {
	payload := fmt.Sprintf("%s:%s:%s:%d", incidentID, playbookName, serviceName, timestamp.Unix())
	mac := hmac.New(sha256.New, r.sharedSecret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func (r *AutomationRouter) Route(ctx context.Context, incidentID string, causal *models.CausalAnalysis, targetService string) {
	if causal == nil || causal.Playbook == nil {
		return
	}

	playbook := causal.Playbook

	if causal.Confidence >= 0.70 {
		log.Printf("AUTOMATION ROUTER: Confidence %.2f is high. Automatically executing playbook %q for service %q", 
			causal.Confidence, playbook.Name, targetService)
		
		playbook.Status = "EXECUTING"
		if err := r.repo.UpdateCausalAnalysis(ctx, incidentID, causal); err != nil {
			log.Printf("AUTOMATION ROUTER: failed to update status to EXECUTING in DB: %v", err)
		}

		ts := time.Now().UTC()
		sig := r.Sign(incidentID, playbook.Name, targetService, ts)

		reqPayload := map[string]string{
			"incident_id":   incidentID,
			"playbook_name": playbook.Name,
			"service_name":  targetService,
			"timestamp":     ts.Format(time.RFC3339),
			"signature":     sig,
		}

		b, err := json.Marshal(reqPayload)
		if err != nil {
			log.Printf("AUTOMATION ROUTER: failed to marshal payload: %v", err)
			r.updateStatus(ctx, incidentID, causal, "FAILED", "Failed to marshal request payload: "+err.Error())
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.agentURL, bytes.NewReader(b))
		if err != nil {
			log.Printf("AUTOMATION ROUTER: failed to create HTTP request: %v", err)
			r.updateStatus(ctx, incidentID, causal, "FAILED", "Failed to create HTTP request: "+err.Error())
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := r.httpClient.Do(req)
		if err != nil {
			log.Printf("AUTOMATION ROUTER: secure agent execution failed: %v", err)
			r.updateStatus(ctx, incidentID, causal, "FAILED", "Secure agent connection failed: "+err.Error())
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("AUTOMATION ROUTER: secure agent returned status %d", resp.StatusCode)
			r.updateStatus(ctx, incidentID, causal, "FAILED", fmt.Sprintf("Secure agent returned status %d", resp.StatusCode))
			return
		}

		var agentResp struct {
			Status string `json:"status"`
			Output string `json:"output"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&agentResp); err != nil {
			log.Printf("AUTOMATION ROUTER: failed to decode agent response: %v", err)
			r.updateStatus(ctx, incidentID, causal, "FAILED", "Failed to decode agent response: "+err.Error())
			return
		}

		log.Printf("AUTOMATION ROUTER: Playbook %q execution result: status=%s, output=%q", 
			playbook.Name, agentResp.Status, agentResp.Output)
		r.updateStatus(ctx, incidentID, causal, agentResp.Status, agentResp.Output)

	} else {
		log.Printf("AUTOMATION ROUTER: Confidence %.2f is low. Playbook %q is suggested but not executed.", 
			causal.Confidence, playbook.Name)
		playbook.Status = "SUGGESTED"
		if err := r.repo.UpdateCausalAnalysis(ctx, incidentID, causal); err != nil {
			log.Printf("AUTOMATION ROUTER: failed to update status to SUGGESTED in DB: %v", err)
		}
	}
}

func (r *AutomationRouter) updateStatus(ctx context.Context, incidentID string, causal *models.CausalAnalysis, status, output string) {
	causal.Playbook.Status = status
	causal.Playbook.Output = output
	if err := r.repo.UpdateCausalAnalysis(ctx, incidentID, causal); err != nil {
		log.Printf("AUTOMATION ROUTER: failed to update playbook status to %s: %v", status, err)
	}
}
