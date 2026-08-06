package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// GitHubPullRequestEvent represents the payload from a GitHub Webhook.
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

// GithubWebhookHandler runs incoming PRs through the SLO-risk evaluator
// (correlation-service) and records the verdict so the Deploy Gates screen can
// show a history. The gate response itself is a GitHub commit-status shape.
type GithubWebhookHandler struct {
	db             *sql.DB
	correlationURL string
	// webhookSecret, when set (GITHUB_WEBHOOK_SECRET), enforces HMAC-SHA256
	// verification of the X-Hub-Signature-256 header — secure-by-default when
	// configured, permissive in local/dev when it isn't.
	webhookSecret string
}

func NewGithubWebhookHandler(db *sql.DB, correlationURL string) *GithubWebhookHandler {
	if correlationURL == "" {
		correlationURL = "http://localhost:8083"
	}
	return &GithubWebhookHandler{
		db:             db,
		correlationURL: strings.TrimRight(correlationURL, "/"),
		webhookSecret:  os.Getenv("GITHUB_WEBHOOK_SECRET"),
	}
}

// verifySignature checks GitHub's X-Hub-Signature-256 HMAC over the raw body.
// Returns true when no secret is configured (dev), or when the signature is valid.
func (h *GithubWebhookHandler) verifySignature(sig string, body []byte) bool {
	if h.webhookSecret == "" {
		return true // not configured; accept (documented dev posture)
	}
	const prefix = "sha256="
	if !strings.HasPrefix(sig, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	// Constant-time compare to avoid leaking timing information.
	return hmac.Equal([]byte(strings.TrimPrefix(sig, prefix)), []byte(expected))
}

func (h *GithubWebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the raw body once — needed both for signature verification and decode.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	if !h.verifySignature(r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "pull_request" && event != "" { // empty allowed for manual curl tests
		writeJSONMap(w, http.StatusOK, map[string]string{"message": "ignoring non-PR event"})
		return
	}

	var payload GitHubPullRequestEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if payload.Action != "opened" && payload.Action != "synchronize" && payload.Action != "" {
		writeJSONMap(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	log.Printf("[deploy-gate] PR #%d: %s in %s (SHA: %s)", payload.PullRequest.Number, payload.PullRequest.Title, payload.Repository.FullName, payload.PullRequest.Head.Sha)

	// Forward to correlation-service's SLO-risk evaluator.
	decision, reason := h.evaluate(payload)

	// Record the verdict (best-effort; a persistence failure must not break the
	// gate response GitHub is waiting on).
	h.persist(payload, decision, reason)

	if decision == "BLOCK" {
		log.Printf("[deploy-gate] PR #%d BLOCKED: predicted SLO violation", payload.PullRequest.Number)
		writeJSONMap(w, http.StatusForbidden, map[string]string{
			"state":       "failure",
			"description": "PulseTrace AI blocked this PR: " + reason,
		})
		return
	}
	writeJSONMap(w, http.StatusOK, map[string]string{
		"state":       "success",
		"description": "PulseTrace AI approved this PR.",
	})
}

// evaluate asks correlation-service whether the PR risks an SLO violation.
// A degraded evaluator fails OPEN (APPROVE) so a scoring outage can't wedge
// every deploy — the gate is advisory, not a hard availability dependency.
func (h *GithubWebhookHandler) evaluate(p GitHubPullRequestEvent) (decision, reason string) {
	reqBody, _ := json.Marshal(map[string]string{"title": p.PullRequest.Title, "body": p.PullRequest.Body})
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(h.correlationURL+"/api/v1/slo/evaluate-pr", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		log.Printf("[deploy-gate] evaluator unavailable, failing open: %v", err)
		return "APPROVE", "evaluator unavailable — approved by default"
	}
	defer resp.Body.Close()

	var evalResult struct {
		Data struct {
			Decision string `json:"decision"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&evalResult); err != nil {
		log.Printf("[deploy-gate] evaluator decode failed, failing open: %v", err)
		return "APPROVE", "evaluator response unreadable — approved by default"
	}
	if strings.ToUpper(evalResult.Data.Decision) == "BLOCK" {
		return "BLOCK", "detected potential SLO violation"
	}
	return "APPROVE", "no violations predicted"
}

func (h *GithubWebhookHandler) persist(p GitHubPullRequestEvent, decision, reason string) {
	if h.db == nil {
		return
	}
	_, err := h.db.Exec(
		`INSERT INTO deploy_gates (id, tenant_id, pr_number, title, author, repo, sha, decision, reason, pr_url)
		 VALUES ($1, 'default', $2, $3, $4, $5, $6, $7, $8, $9)`,
		uuid.New().String(), p.PullRequest.Number, p.PullRequest.Title, p.PullRequest.User.Login,
		p.Repository.FullName, p.PullRequest.Head.Sha, decision, reason, p.PullRequest.URL,
	)
	if err != nil {
		log.Printf("[deploy-gate] failed to record gate for PR #%d: %v", p.PullRequest.Number, err)
	}
}

type deployGateView struct {
	ID        string `json:"id"`
	PRNumber  int    `json:"pr_number"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	Repo      string `json:"repo"`
	SHA       string `json:"sha"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	PRURL     string `json:"pr_url"`
	CreatedAt string `json:"created_at"`
}

// ListGates handles GET /api/v1/deployments/gates — the tenant-scoped feed of
// recent gate decisions the Deploy Gates screen renders.
func (h *GithubWebhookHandler) ListGates(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeJSONMap(w, http.StatusOK, map[string]any{"data": []deployGateView{}})
		return
	}
	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}
	rows, err := h.db.Query(
		`SELECT id, pr_number, title, COALESCE(author,''), COALESCE(repo,''), COALESCE(sha,''),
		        decision, COALESCE(reason,''), COALESCE(pr_url,''), created_at::text
		 FROM deploy_gates WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 100`, tenantID)
	if err != nil {
		http.Error(w, "failed to list deploy gates", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []deployGateView{}
	for rows.Next() {
		var g deployGateView
		if err := rows.Scan(&g.ID, &g.PRNumber, &g.Title, &g.Author, &g.Repo, &g.SHA, &g.Decision, &g.Reason, &g.PRURL, &g.CreatedAt); err != nil {
			continue
		}
		out = append(out, g)
	}
	writeJSONMap(w, http.StatusOK, map[string]any{"data": out})
}

func writeJSONMap(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
