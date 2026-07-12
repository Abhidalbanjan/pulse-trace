package handler

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
)

// GitHubPullRequestEvent represents the payload from a GitHub Webhook
type GitHubPullRequestEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
		URL  string `json:"html_url"`
		Head struct {
			Sha string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type GithubWebhookHandler struct {
	// In production, this would hold dependencies for the AI engine
}

func NewGithubWebhookHandler() *GithubWebhookHandler {
	return &GithubWebhookHandler{}
}

func (h *GithubWebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "pull_request" && event != "" { // event might be empty in direct manual curl tests, let it through for testing
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"message": "ignoring non-PR event"})
		return
	}

	var payload GitHubPullRequestEvent
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if payload.Action != "opened" && payload.Action != "synchronize" && payload.Action != "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	log.Printf("[Gateway-Service] Received GitHub Webhook for PR #%d: %s in %s (SHA: %s)", payload.PullRequest.Number, payload.PullRequest.Title, payload.Repository.FullName, payload.PullRequest.Head.Sha)

	// Forward to correlation-service AI engine for SLO evaluation
	reqBody, _ := json.Marshal(map[string]string{
		"title": payload.PullRequest.Title,
		"body":  payload.PullRequest.Body,
	})
	
	resp, err := http.Post("http://localhost:8083/api/v1/slo/evaluate-pr", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Printf("[Gateway-Service] Failed to contact correlation-service: %v", err)
		http.Error(w, "AI Engine unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	var evalResult struct {
		Data struct {
			Decision string `json:"decision"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&evalResult); err != nil {
		log.Printf("[Gateway-Service] Failed to decode evaluation response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if evalResult.Data.Decision == "BLOCK" {
		log.Printf("[Shift-Left Gate] PR #%d BLOCKED: Predicted SLO violation.", payload.PullRequest.Number)
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{
			"state": "failure",
			"description": "PulseTrace AI blocked this PR: Detected potential SLO violation.",
		})
		return
	}

	log.Printf("[Shift-Left Gate] PR #%d APPROVED: No violations predicted.", payload.PullRequest.Number)
	json.NewEncoder(w).Encode(map[string]string{
		"state": "success",
		"description": "PulseTrace AI approved this PR.",
	})
}
