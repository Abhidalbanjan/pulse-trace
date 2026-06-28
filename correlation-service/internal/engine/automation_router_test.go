package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pulsetrace/shared/models"
)

type mockPlaybookRepo struct {
	calls []models.CausalAnalysis
}

func (m *mockPlaybookRepo) UpdateCausalAnalysis(ctx context.Context, incidentID string, c *models.CausalAnalysis) error {
	copied := *c
	if c.Playbook != nil {
		p := *c.Playbook
		copied.Playbook = &p
	}
	m.calls = append(m.calls, copied)
	return nil
}

func TestAutomationRouter_Sign(t *testing.T) {
	secret := "my_test_secret"
	router := NewAutomationRouter(nil, "", secret)

	incidentID := "inc-1"
	playbookName := "restart_service"
	serviceName := "payment-service"
	ts := time.Now().UTC()

	sig := router.Sign(incidentID, playbookName, serviceName, ts)

	// Recompute signature manually to verify
	payload := fmt.Sprintf("%s:%s:%s:%d", incidentID, playbookName, serviceName, ts.Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if sig != expectedSig {
		t.Errorf("expected signature %s, got %s", expectedSig, sig)
	}
}

func TestAutomationRouter_Route(t *testing.T) {
	secret := "my_test_secret"

	t.Run("High Confidence - Auto Execute Playbook", func(t *testing.T) {
		var receivedReq map[string]string
		agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&receivedReq)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"EXECUTED","output":"Mock rolling restart done"}`))
		}))
		defer agentServer.Close()

		repo := &mockPlaybookRepo{}
		router := NewAutomationRouter(repo, agentServer.URL, secret)

		causal := &models.CausalAnalysis{
			RootCause:  "Kubernetes pod memory leak",
			Confidence: 0.85,
			Playbook: &models.PlaybookAction{
				Name:        "restart_service",
				Description: "Restart pods",
				Status:      "SUGGESTED",
			},
		}

		router.Route(context.Background(), "inc-1", causal, "payment-service")

		// Verify HTTP request payload
		if receivedReq == nil {
			t.Fatal("expected HTTP request to agent, got none")
		}
		if receivedReq["playbook_name"] != "restart_service" {
			t.Errorf("expected playbook_name 'restart_service', got %s", receivedReq["playbook_name"])
		}

		// Verify repo updates
		if len(repo.calls) != 2 { // EXECUTING (before HTTP call) -> EXECUTED (after HTTP call success)
			t.Errorf("expected 2 repo update calls, got %d", len(repo.calls))
		} else {
			if repo.calls[0].Playbook.Status != "EXECUTING" {
				t.Errorf("expected first status update to be EXECUTING, got %s", repo.calls[0].Playbook.Status)
			}
			if repo.calls[1].Playbook.Status != "EXECUTED" {
				t.Errorf("expected final status update to be EXECUTED, got %s", repo.calls[1].Playbook.Status)
			}
			if repo.calls[1].Playbook.Output != "Mock rolling restart done" {
				t.Errorf("expected output 'Mock rolling restart done', got %s", repo.calls[1].Playbook.Output)
			}
		}
	})

	t.Run("Low Confidence - Only Suggest Playbook", func(t *testing.T) {
		repo := &mockPlaybookRepo{}
		router := NewAutomationRouter(repo, "http://invalid-url", secret)

		causal := &models.CausalAnalysis{
			RootCause:  "Minor warning",
			Confidence: 0.45,
			Playbook: &models.PlaybookAction{
				Name:        "restart_service",
				Description: "Restart pods",
				Status:      "SUGGESTED",
			},
		}

		router.Route(context.Background(), "inc-1", causal, "payment-service")

		// Verify only 1 update to SUGGESTED
		if len(repo.calls) != 1 {
			t.Errorf("expected 1 repo update call, got %d", len(repo.calls))
		} else {
			if repo.calls[0].Playbook.Status != "SUGGESTED" {
				t.Errorf("expected status to be SUGGESTED, got %s", repo.calls[0].Playbook.Status)
			}
		}
	})
}
