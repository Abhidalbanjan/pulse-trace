package billing

import (
	"os"
	"strings"

	"github.com/pulsetrace/gateway-service/internal/quota"
)

// Plan catalog (F17): the source of truth for what the in-app plan-comparison
// UI shows. Monthly quota limits come from the quota package — the same numbers
// the enforcer applies — so the pricing page can't drift from reality. Display
// prices are list prices (overridable via BILLING_PRICE_<PLAN>); the authoritative
// charge always comes from Stripe at checkout.

// PlanCTA is the recommended action for a plan relative to the tenant's current
// one, so the UI can render the right button without re-deriving the ordering.
type PlanCTA string

const (
	CTACurrent   PlanCTA = "current"   // the tenant's active plan
	CTAUpgrade   PlanCTA = "upgrade"   // a higher self-serve tier
	CTADowngrade PlanCTA = "downgrade" // a lower tier
	CTAContact   PlanCTA = "contact"   // enterprise / not self-serve
)

// PlanLimits mirrors quota.Limits for JSON, with 0 meaning unlimited.
type PlanLimits struct {
	Traces  int64 `json:"traces"`
	Metrics int64 `json:"metrics"`
	Logs    int64 `json:"logs"`
	RUM     int64 `json:"rum"`
}

// Plan is one card in the comparison.
type Plan struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Price     string     `json:"price"`   // display list price, e.g. "$99"
	Period    string     `json:"period"`  // "mo" (empty for free/enterprise)
	Limits    PlanLimits `json:"limits"`
	Features  []string   `json:"features"`
	CTA       PlanCTA    `json:"cta"`
	SelfServe bool       `json:"self_serve"` // can this plan be selected via Stripe checkout
}

// PlanCatalog is the full response for GET /api/v1/billing/plans.
type PlanCatalog struct {
	CurrentPlan string `json:"current_plan"`
	SelfServe   bool   `json:"self_serve"` // provider supports self-serve checkout at all
	Plans       []Plan `json:"plans"`
}

var planLabels = map[string]string{
	"free": "Free", "standard": "Standard", "premium": "Premium", "enterprise": "Enterprise",
}

// defaultPrices are display list prices; overridable per plan via
// BILLING_PRICE_<PLAN> (e.g. BILLING_PRICE_STANDARD="$149"). Enterprise is
// always "Contact sales" and free is always "$0".
var defaultPrices = map[string]string{
	"free": "$0", "standard": "$99", "premium": "$499", "enterprise": "Contact sales",
}

var planFeatures = map[string][]string{
	"free":       {"1M traces / metrics / logs per month", "Community support", "7-day retention"},
	"standard":   {"20M traces / metrics / logs per month", "Email support", "30-day retention", "SLOs & alerting"},
	"premium":    {"200M traces / metrics / logs per month", "Priority support", "90-day retention", "Causal AI & self-healing"},
	"enterprise": {"Unlimited ingestion", "SSO/SAML, SCIM, audit export", "Custom retention & DR", "Dedicated support"},
}

// selfServePlans are the tiers a customer can move to via Stripe checkout. Free
// and enterprise are not self-serve (downgrade-to-free is done in the portal;
// enterprise is sales-assisted).
var selfServePlans = map[string]bool{"standard": true, "premium": true}

// BuildPlanCatalog assembles the catalog for a tenant's current plan. Pure and
// deterministic: given the current plan and whether the provider is Stripe, the
// per-plan CTA is derived from PlanOrder. providerSelfServe is false for the
// manual/on-prem provider, which flips every self-serve CTA to "contact".
func BuildPlanCatalog(currentPlan string, providerSelfServe bool) PlanCatalog {
	currentPlan = strings.ToLower(strings.TrimSpace(currentPlan))
	if currentPlan == "" {
		currentPlan = "free"
	}
	currentIdx := indexOfPlan(currentPlan)

	plans := make([]Plan, 0, len(quota.PlanOrder))
	for _, id := range quota.PlanOrder {
		lim, _ := quota.LimitsForPlan(id)
		p := Plan{
			ID:        id,
			Label:     planLabels[id],
			Price:     priceFor(id),
			Period:    periodFor(id),
			Limits:    PlanLimits{Traces: lim.Traces, Metrics: lim.Metrics, Logs: lim.Logs, RUM: lim.RUM},
			Features:  planFeatures[id],
			SelfServe: providerSelfServe && selfServePlans[id],
		}
		p.CTA = ctaFor(id, indexOfPlan(id), currentPlan, currentIdx, providerSelfServe)
		plans = append(plans, p)
	}
	return PlanCatalog{CurrentPlan: currentPlan, SelfServe: providerSelfServe, Plans: plans}
}

// ctaFor derives the recommended action for a plan. Without a self-serve
// provider (manual/on-prem billing) every non-current plan is "contact" — plan
// changes there go through the admin override or sales. With Stripe, enterprise
// is still sales-assisted; the rest are upgrade/downgrade by tier order (free is
// reachable by cancelling in the portal).
func ctaFor(id string, idx int, currentPlan string, currentIdx int, providerSelfServe bool) PlanCTA {
	if id == currentPlan {
		return CTACurrent
	}
	if !providerSelfServe || id == "enterprise" {
		return CTAContact
	}
	if idx > currentIdx {
		return CTAUpgrade
	}
	return CTADowngrade
}

func indexOfPlan(plan string) int {
	for i, p := range quota.PlanOrder {
		if p == plan {
			return i
		}
	}
	return 0 // unknown → treat as the lowest tier
}

func priceFor(id string) string {
	if id == "standard" || id == "premium" {
		if v := strings.TrimSpace(os.Getenv("BILLING_PRICE_" + strings.ToUpper(id))); v != "" {
			return v
		}
	}
	return defaultPrices[id]
}

func periodFor(id string) string {
	if id == "standard" || id == "premium" {
		return "mo"
	}
	return ""
}
