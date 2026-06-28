package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleExecutePlaybook_SignatureValidation(t *testing.T) {
	secret := "test_shared_secret"
	api := NewAPI(nil, secret)

	incidentID := "test-incident-123"
	playbookName := "restart_service"
	serviceName := "payment-service"

	t.Run("Valid Signature and Timestamp", func(t *testing.T) {
		ts := time.Now().UTC()
		payload := fmt.Sprintf("%s:%s:%s:%d", incidentID, playbookName, serviceName, ts.Unix())
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(payload))
		sig := hex.EncodeToString(mac.Sum(nil))

		reqBody := SignedPlaybookRequest{
			IncidentID:   incidentID,
			PlaybookName: playbookName,
			ServiceName:  serviceName,
			Timestamp:    ts.Format(time.RFC3339),
			Signature:    sig,
		}

		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/playbook/execute", bytes.NewReader(b))
		rr := httptest.NewRecorder()

		api.handleExecutePlaybook(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rr.Code)
		}

		var resp map[string]string
		json.Unmarshal(rr.Body.Bytes(), &resp)
		if resp["status"] != "EXECUTED" {
			t.Errorf("expected status 'EXECUTED', got %s", resp["status"])
		}
	})

	t.Run("Expired Timestamp", func(t *testing.T) {
		ts := time.Now().UTC().Add(-10 * time.Minute)
		payload := fmt.Sprintf("%s:%s:%s:%d", incidentID, playbookName, serviceName, ts.Unix())
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(payload))
		sig := hex.EncodeToString(mac.Sum(nil))

		reqBody := SignedPlaybookRequest{
			IncidentID:   incidentID,
			PlaybookName: playbookName,
			ServiceName:  serviceName,
			Timestamp:    ts.Format(time.RFC3339),
			Signature:    sig,
		}

		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/playbook/execute", bytes.NewReader(b))
		rr := httptest.NewRecorder()

		api.handleExecutePlaybook(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", rr.Code)
		}
	})

	t.Run("Invalid Signature", func(t *testing.T) {
		ts := time.Now().UTC()
		reqBody := SignedPlaybookRequest{
			IncidentID:   incidentID,
			PlaybookName: playbookName,
			ServiceName:  serviceName,
			Timestamp:    ts.Format(time.RFC3339),
			Signature:    "invalid_hex_signature",
		}

		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/playbook/execute", bytes.NewReader(b))
		rr := httptest.NewRecorder()

		api.handleExecutePlaybook(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", rr.Code)
		}
	})
}
