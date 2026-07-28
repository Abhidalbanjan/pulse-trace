package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pulsetrace/shared/remediation"
)

const testSecret = "test_shared_secret"

// signPlaybook produces the HMAC the agent expects, mirroring
// correlation-service's AutomationRouter.Sign.
func signPlaybook(secret, incidentID, playbookName, serviceName string, ts time.Time, dryRun bool) string {
	payload := fmt.Sprintf("%s:%s:%s:%d:dry_run=%t", incidentID, playbookName, serviceName, ts.Unix(), dryRun)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// executingAgent returns an API pinned to a mode that permits execution, with
// the shell-out stubbed so tests never touch a real kubectl/docker.
func executingAgent(t *testing.T, mode remediation.Mode) (*API, *[][]string) {
	t.Helper()
	api := NewAPIWithPolicy(nil, testSecret, remediation.Policy{
		Mode:                mode,
		ConfidenceThreshold: remediation.DefaultConfidenceThreshold,
	})
	var ran [][]string
	api.runCmd = func(_ context.Context, name string, args ...string) (string, error) {
		ran = append(ran, append([]string{name}, args...))
		return "stubbed ok", nil
	}
	return api, &ran
}

func postPlaybook(t *testing.T, api *API, body SignedPlaybookRequest) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/playbook/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	api.handleExecutePlaybook(rr, req)

	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	return rr, resp
}

func TestHandleExecutePlaybook_SignatureValidation(t *testing.T) {
	incidentID := "test-incident-123"
	playbookName := "restart_service"
	serviceName := "payment-service"

	t.Run("Valid Signature and Timestamp", func(t *testing.T) {
		api, ran := executingAgent(t, remediation.ModeAuto)
		ts := time.Now().UTC()

		rr, resp := postPlaybook(t, api, SignedPlaybookRequest{
			IncidentID:   incidentID,
			PlaybookName: playbookName,
			ServiceName:  serviceName,
			Timestamp:    ts.Format(time.RFC3339),
			Signature:    signPlaybook(testSecret, incidentID, playbookName, serviceName, ts, false),
		})

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rr.Code)
		}
		if resp["status"] != PlaybookStatusExecuted {
			t.Errorf("status = %v, want %s", resp["status"], PlaybookStatusExecuted)
		}
		if len(*ran) == 0 {
			t.Error("expected a command to be run for a live execution")
		}
	})

	t.Run("Expired Timestamp", func(t *testing.T) {
		api, _ := executingAgent(t, remediation.ModeAuto)
		ts := time.Now().UTC().Add(-10 * time.Minute)

		rr, _ := postPlaybook(t, api, SignedPlaybookRequest{
			IncidentID:   incidentID,
			PlaybookName: playbookName,
			ServiceName:  serviceName,
			Timestamp:    ts.Format(time.RFC3339),
			Signature:    signPlaybook(testSecret, incidentID, playbookName, serviceName, ts, false),
		})

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", rr.Code)
		}
	})

	t.Run("Invalid Signature", func(t *testing.T) {
		api, _ := executingAgent(t, remediation.ModeAuto)
		ts := time.Now().UTC()

		rr, _ := postPlaybook(t, api, SignedPlaybookRequest{
			IncidentID:   incidentID,
			PlaybookName: playbookName,
			ServiceName:  serviceName,
			Timestamp:    ts.Format(time.RFC3339),
			Signature:    "invalid_hex_signature",
		})

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", rr.Code)
		}
	})

	t.Run("A dry-run signature cannot be replayed as a live change", func(t *testing.T) {
		// The attack this guards against: capture a legitimate dry-run
		// request, flip dry_run to false, replay it as a production change.
		api, ran := executingAgent(t, remediation.ModeAuto)
		ts := time.Now().UTC()

		rr, _ := postPlaybook(t, api, SignedPlaybookRequest{
			IncidentID:   incidentID,
			PlaybookName: playbookName,
			ServiceName:  serviceName,
			Timestamp:    ts.Format(time.RFC3339),
			DryRun:       false,
			Signature:    signPlaybook(testSecret, incidentID, playbookName, serviceName, ts, true), // signed for dry-run
		})

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d — dry_run must be covered by the HMAC", rr.Code)
		}
		if len(*ran) != 0 {
			t.Errorf("commands ran despite a bad signature: %v", *ran)
		}
	})
}

func TestHandleExecutePlaybook_DryRunChangesNothing(t *testing.T) {
	api, ran := executingAgent(t, remediation.ModeAuto)
	ts := time.Now().UTC()

	rr, resp := postPlaybook(t, api, SignedPlaybookRequest{
		IncidentID:   "inc-1",
		PlaybookName: "restart_service",
		ServiceName:  "payment-service",
		Timestamp:    ts.Format(time.RFC3339),
		DryRun:       true,
		Signature:    signPlaybook(testSecret, "inc-1", "restart_service", "payment-service", ts, true),
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if len(*ran) != 0 {
		t.Errorf("a dry run executed commands: %v", *ran)
	}
	if resp["status"] != PlaybookStatusDryRun {
		t.Errorf("status = %v, want %s", resp["status"], PlaybookStatusDryRun)
	}
	if resp["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", resp["dry_run"])
	}

	// The plan must name the actual command, so an operator can review it.
	output, _ := resp["output"].(string)
	if !strings.Contains(output, "kubectl rollout restart deployment/payment-service") {
		t.Errorf("plan does not describe the real command:\n%s", output)
	}
}

func TestHandleExecutePlaybook_AgentPolicyDowngradesLiveRequests(t *testing.T) {
	// An operator who pins their on-prem agent to dry-run means it. A
	// compromised or misconfigured control plane must not be able to talk it
	// into mutating their infrastructure.
	api, ran := executingAgent(t, remediation.ModeDryRun)
	ts := time.Now().UTC()

	rr, resp := postPlaybook(t, api, SignedPlaybookRequest{
		IncidentID:   "inc-1",
		PlaybookName: "restart_service",
		ServiceName:  "payment-service",
		Timestamp:    ts.Format(time.RFC3339),
		DryRun:       false, // control plane asked for a real execution
		Signature:    signPlaybook(testSecret, "inc-1", "restart_service", "payment-service", ts, false),
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if len(*ran) != 0 {
		t.Errorf("agent executed commands despite a dry-run policy: %v", *ran)
	}
	if resp["dry_run"] != true {
		t.Errorf("dry_run = %v, want true — the response must not claim a change was made", resp["dry_run"])
	}
	if resp["status"] != PlaybookStatusDryRun {
		t.Errorf("status = %v, want %s", resp["status"], PlaybookStatusDryRun)
	}
}

func TestHandleExecutePlaybook_OffPolicyAlsoRefusesToExecute(t *testing.T) {
	api, ran := executingAgent(t, remediation.ModeOff)
	ts := time.Now().UTC()

	_, resp := postPlaybook(t, api, SignedPlaybookRequest{
		IncidentID:   "inc-1",
		PlaybookName: "scale_replicas",
		ServiceName:  "payment-service",
		Timestamp:    ts.Format(time.RFC3339),
		Signature:    signPlaybook(testSecret, "inc-1", "scale_replicas", "payment-service", ts, false),
	})

	if len(*ran) != 0 {
		t.Errorf("agent executed commands while remediation is off: %v", *ran)
	}
	if resp["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", resp["dry_run"])
	}
}

func TestRunPlaybook_PlanMatchesTheRealCommands(t *testing.T) {
	// A dry-run that describes something other than what would really run is
	// worse than no dry-run, because it manufactures false confidence. This
	// asserts the planned command string is the one actually executed.
	cases := []struct {
		playbook string
		wantCmd  []string
	}{
		{"restart_service", []string{"kubectl", "rollout", "restart", "deployment/payment-service"}},
		{"scale_replicas", []string{"kubectl", "scale", "deployment/payment-service", "--replicas=4"}},
	}

	for _, tc := range cases {
		t.Run(tc.playbook, func(t *testing.T) {
			api, ran := executingAgent(t, remediation.ModeAuto)
			req := SignedPlaybookRequest{PlaybookName: tc.playbook, ServiceName: "payment-service"}

			_, plan := api.runPlaybook(context.Background(), req, true)
			if len(*ran) != 0 {
				t.Fatalf("dry run executed commands: %v", *ran)
			}

			status, _ := api.runPlaybook(context.Background(), req, false)
			if status != PlaybookStatusExecuted {
				t.Fatalf("live run status = %q, want %q", status, PlaybookStatusExecuted)
			}
			if len(*ran) != 1 {
				t.Fatalf("live run executed %d commands, want 1: %v", len(*ran), *ran)
			}

			executed := strings.Join((*ran)[0], " ")
			if executed != strings.Join(tc.wantCmd, " ") {
				t.Fatalf("executed %q, want %q", executed, strings.Join(tc.wantCmd, " "))
			}
			if !strings.Contains(plan, executed) {
				t.Errorf("the plan does not contain the command that actually ran.\nplan:\n%s\nran: %s", plan, executed)
			}
		})
	}
}

func TestRunPlaybook_UnknownPlaybookFailsEvenInDryRun(t *testing.T) {
	// Reporting "nothing to do" would hide the misconfiguration.
	api, _ := executingAgent(t, remediation.ModeAuto)
	req := SignedPlaybookRequest{PlaybookName: "reboot_the_datacenter", ServiceName: "payment-service"}

	status, output := api.runPlaybook(context.Background(), req, true)
	if status != PlaybookStatusFailed {
		t.Errorf("status = %q, want %q", status, PlaybookStatusFailed)
	}
	if !strings.Contains(output, "reboot_the_datacenter") {
		t.Errorf("output should name the unknown playbook: %q", output)
	}
}
