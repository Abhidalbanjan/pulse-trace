package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/remediation"
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

func (m *mockPlaybookRepo) statuses() []string {
	out := make([]string, 0, len(m.calls))
	for _, c := range m.calls {
		out = append(out, c.Playbook.Status)
	}
	return out
}

// agentStub stands in for topology-service's signed playbook endpoint,
// capturing what the router asked it to do.
type agentStub struct {
	*httptest.Server
	received map[string]any
}

func newAgentStub(t *testing.T, status, output string, dryRun bool) *agentStub {
	t.Helper()
	stub := &agentStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&stub.received)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": status, "output": output, "dry_run": dryRun,
		})
	}))
	t.Cleanup(stub.Close)
	return stub
}

func highConfidenceCausal() *models.CausalAnalysis {
	return &models.CausalAnalysis{
		RootCause:  "Kubernetes pod memory leak",
		Confidence: 0.85,
		Playbook: &models.PlaybookAction{
			Name:        "restart_service",
			Description: "Restart pods",
			Status:      models.PlaybookSuggested,
		},
	}
}

func routerWithMode(t *testing.T, repo PlaybookRepository, agentURL, secret string, mode remediation.Mode) *AutomationRouter {
	t.Helper()
	return NewAutomationRouterWithPolicy(repo, agentURL, secret, remediation.Policy{
		Mode:                mode,
		ConfidenceThreshold: remediation.DefaultConfidenceThreshold,
	})
}

func TestAutomationRouter_Sign(t *testing.T) {
	secret := "my_test_secret"
	router := NewAutomationRouter(nil, "", secret)

	incidentID := "inc-1"
	playbookName := "restart_service"
	serviceName := "payment-service"
	ts := time.Now().UTC()

	sig := router.Sign(incidentID, playbookName, serviceName, ts, false)

	payload := fmt.Sprintf("%s:%s:%s:%d:dry_run=false", incidentID, playbookName, serviceName, ts.Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if sig != expectedSig {
		t.Errorf("expected signature %s, got %s", expectedSig, sig)
	}
}

func TestAutomationRouter_SignCoversDryRunFlag(t *testing.T) {
	// The dangerous direction is an attacker flipping a dry run into a real
	// change. If dry_run rode outside the signature, a captured dry-run
	// request could be replayed as a live production change.
	router := NewAutomationRouter(nil, "", "secret")
	ts := time.Now().UTC()

	dry := router.Sign("inc-1", "restart_service", "payment-service", ts, true)
	live := router.Sign("inc-1", "restart_service", "payment-service", ts, false)

	if dry == live {
		t.Error("signature is identical for dry-run and live requests — the flag is not covered by the HMAC")
	}
}

// ── Policy routing ─────────────────────────────────────────────────────────

func TestRouteDefaultPolicyRequiresApproval(t *testing.T) {
	// The headline behaviour change: a high-confidence playbook under the
	// default policy must NOT touch production.
	agent := newAgentStub(t, "EXECUTED", "should never be called", false)
	repo := &mockPlaybookRepo{}
	router := NewAutomationRouter(repo, agent.URL, "secret")

	router.Route(context.Background(), "inc-1", highConfidenceCausal(), "payment-service")

	if agent.received != nil {
		t.Errorf("agent was called with %v — the default policy must not execute without approval", agent.received)
	}
	if got := repo.statuses(); len(got) != 1 || got[0] != models.PlaybookPendingApproval {
		t.Errorf("statuses = %v, want [%s]", got, models.PlaybookPendingApproval)
	}
}

func TestRouteAutoModeExecutes(t *testing.T) {
	agent := newAgentStub(t, models.PlaybookExecuted, "Mock rolling restart done", false)
	repo := &mockPlaybookRepo{}
	router := routerWithMode(t, repo, agent.URL, "secret", remediation.ModeAuto)

	router.Route(context.Background(), "inc-1", highConfidenceCausal(), "payment-service")

	if agent.received == nil {
		t.Fatal("expected the agent to be called in auto mode")
	}
	if agent.received["playbook_name"] != "restart_service" {
		t.Errorf("playbook_name = %v, want restart_service", agent.received["playbook_name"])
	}
	if agent.received["dry_run"] != false {
		t.Errorf("dry_run = %v, want false for a live auto execution", agent.received["dry_run"])
	}

	want := []string{models.PlaybookExecuting, models.PlaybookExecuted}
	if got := repo.statuses(); !equalStrings(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	if last := repo.calls[len(repo.calls)-1].Playbook; last.Output != "Mock rolling restart done" {
		t.Errorf("output = %q", last.Output)
	}
}

func TestRouteLowConfidenceOnlySuggests(t *testing.T) {
	agent := newAgentStub(t, "EXECUTED", "should never be called", false)
	repo := &mockPlaybookRepo{}
	router := routerWithMode(t, repo, agent.URL, "secret", remediation.ModeAuto)

	causal := highConfidenceCausal()
	causal.Confidence = 0.45

	router.Route(context.Background(), "inc-1", causal, "payment-service")

	if agent.received != nil {
		t.Error("agent was called for a below-threshold hypothesis")
	}
	if got := repo.statuses(); len(got) != 1 || got[0] != models.PlaybookSuggested {
		t.Errorf("statuses = %v, want [%s]", got, models.PlaybookSuggested)
	}
}

func TestRouteLowConfidenceDoesNotAskForApproval(t *testing.T) {
	// Approval fatigue is how a human-in-the-loop control stops being a real
	// control, so weak hypotheses must not generate approval requests.
	repo := &mockPlaybookRepo{}
	router := NewAutomationRouter(repo, "http://invalid", "secret")

	causal := highConfidenceCausal()
	causal.Confidence = 0.30

	router.Route(context.Background(), "inc-1", causal, "payment-service")

	if got := repo.statuses(); len(got) != 1 || got[0] != models.PlaybookSuggested {
		t.Errorf("statuses = %v, want [%s]", got, models.PlaybookSuggested)
	}
}

func TestRouteOffModeSuppresses(t *testing.T) {
	agent := newAgentStub(t, "EXECUTED", "should never be called", false)
	repo := &mockPlaybookRepo{}
	router := routerWithMode(t, repo, agent.URL, "secret", remediation.ModeOff)

	router.Route(context.Background(), "inc-1", highConfidenceCausal(), "payment-service")

	if agent.received != nil {
		t.Error("agent was called while remediation is off")
	}
	if got := repo.statuses(); len(got) != 1 || got[0] != models.PlaybookSuppressed {
		t.Errorf("statuses = %v, want [%s]", got, models.PlaybookSuppressed)
	}
}

func TestRouteDryRunModePlansWithoutExecuting(t *testing.T) {
	agent := newAgentStub(t, models.PlaybookDryRun, "would run: kubectl rollout restart deployment/payment-service", true)
	repo := &mockPlaybookRepo{}
	router := routerWithMode(t, repo, agent.URL, "secret", remediation.ModeDryRun)

	router.Route(context.Background(), "inc-1", highConfidenceCausal(), "payment-service")

	if agent.received == nil {
		t.Fatal("expected the agent to be called to compute the plan")
	}
	if agent.received["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", agent.received["dry_run"])
	}

	last := repo.calls[len(repo.calls)-1].Playbook
	if last.Status != models.PlaybookDryRun {
		t.Errorf("final status = %q, want %q", last.Status, models.PlaybookDryRun)
	}
	if !last.DryRun {
		t.Error("DryRun flag = false, want true — the incident timeline must not imply anything changed")
	}
}

func TestRouteTrustsTheAgentsAccountOfWhatItDid(t *testing.T) {
	// An on-prem agent pinned to dry-run will refuse a live request. Recording
	// that as a completed remediation would be a lie in the incident timeline.
	agent := newAgentStub(t, models.PlaybookDryRun, "agent is pinned to dry-run; nothing was changed", true)
	repo := &mockPlaybookRepo{}
	router := routerWithMode(t, repo, agent.URL, "secret", remediation.ModeAuto)

	router.Route(context.Background(), "inc-1", highConfidenceCausal(), "payment-service")

	if agent.received["dry_run"] != false {
		t.Fatalf("router asked for dry_run=%v, want false (auto mode)", agent.received["dry_run"])
	}
	last := repo.calls[len(repo.calls)-1].Playbook
	if !last.DryRun {
		t.Error("DryRun = false, want true — the agent said it changed nothing")
	}
	if last.Status != models.PlaybookDryRun {
		t.Errorf("status = %q, want %q", last.Status, models.PlaybookDryRun)
	}
}

func TestRouteRecordsAgentFailure(t *testing.T) {
	repo := &mockPlaybookRepo{}
	// An unroutable agent URL: the dispatch must fail loudly on the incident
	// rather than leaving the playbook stuck in EXECUTING forever.
	router := routerWithMode(t, repo, "http://127.0.0.1:1/agent", "secret", remediation.ModeAuto)

	router.Route(context.Background(), "inc-1", highConfidenceCausal(), "payment-service")

	got := repo.statuses()
	if len(got) != 2 || got[1] != models.PlaybookFailed {
		t.Errorf("statuses = %v, want the run to end in %s", got, models.PlaybookFailed)
	}
}

func TestRouteIgnoresIncidentsWithoutAPlaybook(t *testing.T) {
	repo := &mockPlaybookRepo{}
	router := routerWithMode(t, repo, "http://invalid", "secret", remediation.ModeAuto)

	router.Route(context.Background(), "inc-1", nil, "payment-service")
	router.Route(context.Background(), "inc-1", &models.CausalAnalysis{Confidence: 0.99}, "payment-service")

	if len(repo.calls) != 0 {
		t.Errorf("repo was called %d times for incidents with no playbook", len(repo.calls))
	}
}

// ── Approval flow ──────────────────────────────────────────────────────────

func TestApproveExecutesAndRecordsWhoAuthorizedIt(t *testing.T) {
	agent := newAgentStub(t, models.PlaybookExecuted, "restarted", false)
	repo := &mockPlaybookRepo{}
	router := NewAutomationRouter(repo, agent.URL, "secret")

	causal := highConfidenceCausal()
	router.Route(context.Background(), "inc-1", causal, "payment-service")

	if err := router.Approve(context.Background(), "inc-1", causal, "payment-service", "abhishek@example.com"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if agent.received == nil {
		t.Fatal("expected the agent to be called after approval")
	}
	if agent.received["dry_run"] != false {
		t.Errorf("dry_run = %v, want false for an approved execution", agent.received["dry_run"])
	}

	last := repo.calls[len(repo.calls)-1].Playbook
	if last.Status != models.PlaybookExecuted {
		t.Errorf("status = %q, want %q", last.Status, models.PlaybookExecuted)
	}
	if last.ApprovedBy != "abhishek@example.com" {
		t.Errorf("ApprovedBy = %q, want the approver's identity", last.ApprovedBy)
	}
	if last.ApprovedAt == nil {
		t.Error("ApprovedAt = nil, want a timestamp")
	}
}

func TestApproveRecordsTheApproverBeforeExecuting(t *testing.T) {
	// "Who authorized this" must be answerable even when "did it finish" is
	// no — so the identity is persisted before the agent is dialed.
	repo := &mockPlaybookRepo{}
	router := NewAutomationRouter(repo, "http://127.0.0.1:1/agent", "secret")

	causal := highConfidenceCausal()
	router.Route(context.Background(), "inc-1", causal, "payment-service")

	if err := router.Approve(context.Background(), "inc-1", causal, "payment-service", "abhishek@example.com"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	// calls: PENDING_APPROVAL, EXECUTING, FAILED — the EXECUTING write must
	// already carry the approver.
	if len(repo.calls) < 2 {
		t.Fatalf("expected at least 2 repo writes, got %d", len(repo.calls))
	}
	executing := repo.calls[1].Playbook
	if executing.Status != models.PlaybookExecuting {
		t.Fatalf("second write status = %q, want %q", executing.Status, models.PlaybookExecuting)
	}
	if executing.ApprovedBy != "abhishek@example.com" {
		t.Error("the EXECUTING write did not carry the approver — a crash here would lose the audit trail")
	}
}

func TestApproveRejectsAPlaybookThatIsNotPending(t *testing.T) {
	repo := &mockPlaybookRepo{}
	router := NewAutomationRouter(repo, "http://invalid", "secret")

	causal := highConfidenceCausal()
	causal.Playbook.Status = models.PlaybookExecuted

	err := router.Approve(context.Background(), "inc-1", causal, "payment-service", "someone")
	if !errors.Is(err, ErrNotAwaitingApproval) {
		t.Errorf("err = %v, want ErrNotAwaitingApproval — approving twice must not re-run a remediation", err)
	}
	if len(repo.calls) != 0 {
		t.Error("repo was written to for an invalid approval")
	}
}

func TestApproveIsRefusedWhenThePolicyNoLongerPermitsExecution(t *testing.T) {
	// The policy was tightened to dry-run after this playbook was proposed.
	// An approval recorded under the old posture doesn't override the new one.
	repo := &mockPlaybookRepo{}
	router := routerWithMode(t, repo, "http://invalid", "secret", remediation.ModeDryRun)

	causal := highConfidenceCausal()
	causal.Playbook.Status = models.PlaybookPendingApproval

	if err := router.Approve(context.Background(), "inc-1", causal, "payment-service", "someone"); err == nil {
		t.Error("expected approval to be refused under a dry-run policy")
	}
}

func TestRejectRecordsWhoDeclined(t *testing.T) {
	repo := &mockPlaybookRepo{}
	router := NewAutomationRouter(repo, "http://invalid", "secret")

	causal := highConfidenceCausal()
	router.Route(context.Background(), "inc-1", causal, "payment-service")

	if err := router.Reject(context.Background(), "inc-1", causal, "abhishek@example.com", "restarting would lose in-flight orders"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	last := repo.calls[len(repo.calls)-1].Playbook
	if last.Status != models.PlaybookRejected {
		t.Errorf("status = %q, want %q", last.Status, models.PlaybookRejected)
	}
	if last.RejectedBy != "abhishek@example.com" || last.RejectedAt == nil {
		t.Errorf("rejection audit trail missing: %+v", last)
	}
	if last.Output == "" {
		t.Error("the rejection reason was not recorded")
	}
}

func TestRejectRefusesAPlaybookThatIsNotPending(t *testing.T) {
	repo := &mockPlaybookRepo{}
	router := NewAutomationRouter(repo, "http://invalid", "secret")

	causal := highConfidenceCausal()
	causal.Playbook.Status = models.PlaybookSuggested

	if err := router.Reject(context.Background(), "inc-1", causal, "someone", ""); !errors.Is(err, ErrNotAwaitingApproval) {
		t.Errorf("err = %v, want ErrNotAwaitingApproval", err)
	}
}

func TestOnDemandDryRunDoesNotRequireAPermissivePolicy(t *testing.T) {
	// "Show me what this would do" is safe under any policy, because it never
	// executes.
	agent := newAgentStub(t, models.PlaybookDryRun, "would run: kubectl rollout restart", true)
	repo := &mockPlaybookRepo{}
	router := routerWithMode(t, repo, agent.URL, "secret", remediation.ModeOff)

	causal := highConfidenceCausal()
	if err := router.DryRun(context.Background(), "inc-1", causal, "payment-service"); err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	if agent.received == nil {
		t.Fatal("expected the agent to be called to compute the plan")
	}
	if agent.received["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", agent.received["dry_run"])
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
