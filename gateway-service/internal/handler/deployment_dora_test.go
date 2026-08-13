package handler

import (
	"testing"
	"time"
)

func ptr(t time.Time) *time.Time { return &t }

func TestComputeDORA(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	deploys := []doraDeploy{
		{Service: "payment", DeployedAt: base},                    // caused an incident (below)
		{Service: "payment", DeployedAt: base.Add(48 * time.Hour)}, // clean
		{Service: "cart", DeployedAt: base.Add(72 * time.Hour)},    // clean
		{Service: "checkout", DeployedAt: base.Add(96 * time.Hour)},// caused an incident
	}
	incidents := []doraIncident{
		// 30 min after the first payment deploy → change failure.
		{Services: []string{"payment"}, StartedAt: base.Add(30 * time.Minute), ResolvedAt: ptr(base.Add(90 * time.Minute))}, // MTTR 60m
		// 15 min after checkout deploy → change failure, resolved in 30m.
		{Services: []string{"checkout"}, StartedAt: base.Add(96*time.Hour + 15*time.Minute), ResolvedAt: ptr(base.Add(96*time.Hour + 45*time.Minute))},
		// Unrelated incident far from any deploy, still open.
		{Services: []string{"search"}, StartedAt: base.Add(200 * time.Hour), ResolvedAt: nil},
	}

	m := computeDORA(deploys, incidents, 10, doraFailureWindow)

	if m.TotalDeploys != 4 {
		t.Errorf("total deploys = %d, want 4", m.TotalDeploys)
	}
	if m.DeployFreqPerDay != 0.4 { // 4 / 10 days
		t.Errorf("deploy freq = %v, want 0.4", m.DeployFreqPerDay)
	}
	if m.FailedDeploys != 2 {
		t.Errorf("failed deploys = %d, want 2 (payment + checkout)", m.FailedDeploys)
	}
	if m.ChangeFailureRatePct != 50 {
		t.Errorf("CFR = %v, want 50", m.ChangeFailureRatePct)
	}
	if m.ResolvedIncidents != 2 { // the open one excluded
		t.Errorf("resolved incidents = %d, want 2", m.ResolvedIncidents)
	}
	if m.MTTRMinutes != 45 { // (60 + 30) / 2
		t.Errorf("MTTR = %v, want 45", m.MTTRMinutes)
	}
}

func TestDeployCausedIncident_WindowAndService(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	d := doraDeploy{Service: "api", DeployedAt: base}
	// Same service, inside window → linked.
	if !deployCausedIncident(d, []doraIncident{{Services: []string{"api"}, StartedAt: base.Add(30 * time.Minute)}}, doraFailureWindow) {
		t.Error("incident 30m after on same service should link")
	}
	// Same service but outside the window → not linked.
	if deployCausedIncident(d, []doraIncident{{Services: []string{"api"}, StartedAt: base.Add(5 * time.Hour)}}, doraFailureWindow) {
		t.Error("incident 5h later must not link")
	}
	// Inside window but different service → not linked.
	if deployCausedIncident(d, []doraIncident{{Services: []string{"web"}, StartedAt: base.Add(10 * time.Minute)}}, doraFailureWindow) {
		t.Error("different service must not link")
	}
	// Incident BEFORE the deploy → not linked.
	if deployCausedIncident(d, []doraIncident{{Services: []string{"api"}, StartedAt: base.Add(-10 * time.Minute)}}, doraFailureWindow) {
		t.Error("incident before the deploy must not link")
	}
}

func TestDORARatings(t *testing.T) {
	if rateDeployFreq(2) != "elite" || rateDeployFreq(0.2) != "high" || rateDeployFreq(0.05) != "medium" || rateDeployFreq(0.001) != "low" {
		t.Error("deploy-freq rating bands wrong")
	}
	if rateChangeFailure(10) != "elite" || rateChangeFailure(25) != "high" || rateChangeFailure(40) != "medium" || rateChangeFailure(60) != "low" {
		t.Error("CFR rating bands wrong")
	}
	if rateMTTR(0, 0) != "n/a" || rateMTTR(30, 1) != "elite" || rateMTTR(300, 1) != "high" || rateMTTR(3*24*60, 1) != "medium" || rateMTTR(10*24*60, 1) != "low" {
		t.Error("MTTR rating bands wrong")
	}
}
