package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pulsetrace/shared/models"
)

type SaaSForwarder struct {
	controlPlaneURL string
	hybridMode      bool
	client          *http.Client
}

func NewSaaSForwarder() *SaaSForwarder {
	return &SaaSForwarder{
		controlPlaneURL: os.Getenv("CONTROL_PLANE_URL"),
		hybridMode:      os.Getenv("HYBRID_MODE") == "true",
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

type AnonymizedCausalLink struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Evidence string `json:"evidence_anonymized"`
}

type AnonymizedIncident struct {
	ID          string                 `json:"incident_id"`
	TenantID    string                 `json:"tenant_id"`
	Status      string                 `json:"status"`
	Severity    string                 `json:"severity"`
	AlertCount  int                    `json:"alert_count"`
	StartedAt   time.Time              `json:"started_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	Confidence  float64                `json:"confidence"`
	CausalChain []AnonymizedCausalLink `json:"causal_chain"`
	Topology    []string               `json:"anonymized_services"`
}

func (f *SaaSForwarder) ForwardIncident(ctx context.Context, incident *models.Incident, alerts []models.IncidentAlert) {
	if !f.hybridMode || f.controlPlaneURL == "" {
		return
	}

	// 1. Anonymize services via hashing
	anonymizeSvc := func(svc string) string {
		h := sha256.Sum256([]byte(svc))
		return "svc_" + hex.EncodeToString(h[:4])
	}

	anonymizedServices := make([]string, 0, len(incident.ServiceNames))
	for _, svc := range incident.ServiceNames {
		anonymizedServices = append(anonymizedServices, anonymizeSvc(svc))
	}

	var anonChain []AnonymizedCausalLink
	if incident.Causal != nil && len(incident.Causal.Chain) > 0 {
		for _, link := range incident.Causal.Chain {
			anonChain = append(anonChain, AnonymizedCausalLink{
				From:     anonymizeSvc(link.FromService),
				To:       anonymizeSvc(link.ToService),
				Evidence: "[MASKED_EVIDENCE_PII_LOGS_STRIPPED]",
			})
		}
	}

	payload := AnonymizedIncident{
		ID:          incident.ID,
		TenantID:    incident.TenantID,
		Status:      string(incident.Status),
		Severity:    string(incident.Severity),
		AlertCount:  incident.AlertCount,
		StartedAt:   incident.StartedAt,
		UpdatedAt:   incident.UpdatedAt,
		Confidence:  incident.Causal.Confidence,
		CausalChain: anonChain,
		Topology:    anonymizedServices,
	}

	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf("SaaSForwarder: failed to marshal anonymized incident: %v", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", f.controlPlaneURL, bytes.NewReader(b))
	if err != nil {
		log.Printf("SaaSForwarder: failed to create request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		log.Printf("SaaSForwarder: failed to send incident to SaaS Control Plane: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		log.Printf("SaaSForwarder: SaaS Control Plane returned non-OK status: %s", resp.Status)
	} else {
		log.Printf("SaaSForwarder: successfully forwarded anonymized incident %s to SaaS Control Plane", incident.ID)
	}
}
