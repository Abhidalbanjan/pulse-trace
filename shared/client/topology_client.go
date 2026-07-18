package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/pulsetrace/shared/models"
)

type TopologyClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewTopologyClient(baseURL string) *TopologyClient {
	return &TopologyClient{
		baseURL:    baseURL,
		httpClient: &http.Client{},
	}
}

// GetDownstreamDependencies returns the immediate downstream services for a given service.
func (c *TopologyClient) GetDownstreamDependencies(ctx context.Context, serviceName string) ([]string, error) {
	url := fmt.Sprintf("%s/api/v1/topology/dependencies/downstream/%s", c.baseURL, serviceName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var deps []string
	if err := json.NewDecoder(resp.Body).Decode(&deps); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return deps, nil
}

// GetUpstreamDependencies returns the immediate upstream services for a given service.
func (c *TopologyClient) GetUpstreamDependencies(ctx context.Context, serviceName string) ([]string, error) {
	url := fmt.Sprintf("%s/api/v1/topology/dependencies/upstream/%s", c.baseURL, serviceName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var deps []string
	if err := json.NewDecoder(resp.Body).Decode(&deps); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return deps, nil
}

// UpdateServiceState updates the state of a service (e.g., PREDICTIVE_WARNING).
func (c *TopologyClient) UpdateServiceState(ctx context.Context, serviceName, state string) error {
	url := fmt.Sprintf("%s/api/v1/topology/state", c.baseURL)
	body := map[string]string{
		"service_name": serviceName,
		"state":        state,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}

// UpdateCausalPath sets the active causal path edges in Neo4j topology, scoped
// to incidentID so concurrently-analyzed incidents never clobber each other's
// causal highlighting (see topology-service's Neo4jRepository.UpdateCausalPath).
func (c *TopologyClient) UpdateCausalPath(ctx context.Context, incidentID string, chain []models.CausalLink) error {
	type Link struct {
		Source string `json:"source"`
		Target string `json:"target"`
		Reason string `json:"reason"`
	}
	type request struct {
		IncidentID string `json:"incident_id"`
		Links      []Link `json:"links"`
	}
	var links []Link
	for _, l := range chain {
		links = append(links, Link{
			Source: l.FromService,
			Target: l.ToService,
			Reason: l.Evidence,
		})
	}
	payload := request{IncidentID: incidentID, Links: links}

	url := fmt.Sprintf("%s/api/v1/topology/causal-path", c.baseURL)
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}
