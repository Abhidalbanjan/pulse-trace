package billing

import "testing"

func ctaByID(cat PlanCatalog, id string) PlanCTA {
	for _, p := range cat.Plans {
		if p.ID == id {
			return p.CTA
		}
	}
	return ""
}

func TestBuildPlanCatalog_CTAsFromCurrentPlan(t *testing.T) {
	cat := BuildPlanCatalog("standard", true)
	if cat.CurrentPlan != "standard" {
		t.Fatalf("current plan not echoed: %s", cat.CurrentPlan)
	}
	if got := ctaByID(cat, "standard"); got != CTACurrent {
		t.Errorf("standard should be current, got %s", got)
	}
	if got := ctaByID(cat, "premium"); got != CTAUpgrade {
		t.Errorf("premium should be an upgrade from standard, got %s", got)
	}
	if got := ctaByID(cat, "free"); got != CTADowngrade {
		t.Errorf("free should be a downgrade from standard, got %s", got)
	}
	if got := ctaByID(cat, "enterprise"); got != CTAContact {
		t.Errorf("enterprise should be contact, got %s", got)
	}
}

func TestBuildPlanCatalog_ManualProviderHasNoSelfServe(t *testing.T) {
	cat := BuildPlanCatalog("free", false) // manual/on-prem provider
	if cat.SelfServe {
		t.Fatal("manual provider must not advertise self-serve")
	}
	for _, p := range cat.Plans {
		if p.SelfServe {
			t.Errorf("plan %s must not be self-serve under manual billing", p.ID)
		}
		// With no self-serve, an otherwise-upgradeable paid tier becomes contact.
		if p.ID == "premium" && p.CTA != CTAContact {
			t.Errorf("premium should be contact under manual billing, got %s", p.CTA)
		}
	}
}

func TestBuildPlanCatalog_LimitsMatchQuota(t *testing.T) {
	cat := BuildPlanCatalog("free", true)
	for _, p := range cat.Plans {
		if p.ID == "standard" && p.Limits.Traces != 20_000_000 {
			t.Errorf("standard trace limit drifted from quota: %d", p.Limits.Traces)
		}
		if p.ID == "enterprise" && p.Limits.Traces != 0 {
			t.Errorf("enterprise should be unlimited (0), got %d", p.Limits.Traces)
		}
	}
	if len(cat.Plans) != 4 {
		t.Fatalf("expected 4 plans, got %d", len(cat.Plans))
	}
}

func TestBuildPlanCatalog_DefaultsCurrentToFree(t *testing.T) {
	cat := BuildPlanCatalog("", true)
	if cat.CurrentPlan != "free" || ctaByID(cat, "free") != CTACurrent {
		t.Fatalf("empty plan should default to free-current, got %s", cat.CurrentPlan)
	}
}

func TestPriceOverrideFromEnv(t *testing.T) {
	t.Setenv("BILLING_PRICE_STANDARD", "$149")
	if got := priceFor("standard"); got != "$149" {
		t.Fatalf("expected env price override, got %s", got)
	}
	if got := priceFor("free"); got != "$0" {
		t.Fatalf("free price should be $0, got %s", got)
	}
}
