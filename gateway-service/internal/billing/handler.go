package billing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/pulsetrace/gateway-service/internal/auth"
)

// Handler wires the billing provider to HTTP endpoints and applies the resulting
// plan/status changes to the tenants table.
type Handler struct {
	provider  Provider
	stripe    *StripeProvider // non-nil only when provider is Stripe (webhook route)
	tenants   *auth.TenantStore
	returnURL string
}

func NewHandler(provider Provider, tenants *auth.TenantStore) *Handler {
	h := &Handler{provider: provider, tenants: tenants, returnURL: envOr("PUBLIC_BASE_URL", "http://localhost:3000") + "/settings"}
	if sp, ok := provider.(*StripeProvider); ok {
		h.stripe = sp
	}
	return h
}

// IsStripe reports whether the active provider is Stripe (so the caller only
// registers the webhook route, and hides the operator plan override, in SaaS mode).
func (h *Handler) IsStripe() bool { return h.stripe != nil }

// Plans handles GET /api/v1/billing/plans — the plan-comparison catalog for the
// caller's tenant, with per-plan upgrade/downgrade CTAs derived from its current
// plan. Read-only; the quota limits shown are exactly what the enforcer applies.
func (h *Handler) Plans(w http.ResponseWriter, r *http.Request) {
	plan := "free"
	if t, err := h.tenants.GetTenant(r.Context(), tenantOf(r)); err == nil && t != nil && t.Plan != "" {
		plan = t.Plan
	}
	writeJSON(w, BuildPlanCatalog(plan, h.IsStripe()))
}

func tenantOf(r *http.Request) string {
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return "default"
}

// Checkout handles POST /api/v1/billing/checkout {plan} — starts a subscription
// checkout for the caller's tenant and returns a redirect URL.
func (h *Handler) Checkout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Plan string `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Plan != "standard" && req.Plan != "premium" {
		http.Error(w, "plan must be 'standard' or 'premium'", http.StatusBadRequest)
		return
	}

	tenantID := tenantOf(r)
	tenant, err := h.tenants.GetTenant(r.Context(), tenantID)
	if err != nil || tenant == nil {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	email := r.Header.Get("X-User-Subject")

	redirectURL, err := h.provider.Checkout(r.Context(), tenantID, tenant.StripeCustomerID, email, req.Plan)
	if err != nil {
		h.writeProviderError(w, err)
		return
	}
	writeJSON(w, map[string]string{"url": redirectURL})
}

// Portal handles POST /api/v1/billing/portal — a self-service billing management URL.
func (h *Handler) Portal(w http.ResponseWriter, r *http.Request) {
	tenant, err := h.tenants.GetTenant(r.Context(), tenantOf(r))
	if err != nil || tenant == nil {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	url, err := h.provider.Portal(r.Context(), tenant.StripeCustomerID, h.returnURL)
	if err != nil {
		h.writeProviderError(w, err)
		return
	}
	writeJSON(w, map[string]string{"url": url})
}

// SetPlan handles POST /api/v1/admin/tenant/plan {plan} — an OPERATOR override for
// manual/on-prem billing. It is only registered when the provider is manual, so a
// SaaS tenant can't self-upgrade for free; there, plan changes come from webhooks.
func (h *Handler) SetPlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Plan string `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !validPlan(req.Plan) {
		http.Error(w, "plan must be one of: free, standard, premium, enterprise", http.StatusBadRequest)
		return
	}
	if err := h.tenants.UpdatePlan(r.Context(), tenantOf(r), req.Plan); err != nil {
		http.Error(w, "failed to update plan", http.StatusInternalServerError)
		return
	}
	auth.WriteAudit(h.tenants.DB(), r.Header.Get("X-User-Subject"), "update", "tenant_plan", tenantOf(r), nil, map[string]string{"plan": req.Plan})
	writeJSON(w, map[string]string{"status": "updated", "plan": req.Plan})
}

// Webhook handles POST /api/v1/webhooks/stripe — Stripe subscription lifecycle
// events, verified and applied to the tenant's plan/status.
func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	if h.stripe == nil {
		http.Error(w, "stripe not configured", http.StatusServiceUnavailable)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	evt, err := h.stripe.VerifyAndParseWebhook(payload, r.Header.Get("Stripe-Signature"))
	if err != nil {
		log.Printf("billing: webhook verification failed: %v", err)
		http.Error(w, "invalid webhook", http.StatusBadRequest)
		return
	}

	if err := h.applyEvent(r.Context(), evt); err != nil {
		log.Printf("billing: failed to apply %s for tenant %q: %v", evt.Type, evt.TenantID, err)
		// 500 so Stripe retries; the event itself was valid.
		http.Error(w, "failed to apply event", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"received": "true"})
}

// applyEvent maps a verified Stripe event onto the tenant's plan/status.
func (h *Handler) applyEvent(ctx context.Context, evt *WebhookEvent) error {
	tenantID := evt.TenantID
	if tenantID == "" && evt.CustomerID != "" {
		if t, _ := h.tenants.TenantByStripeCustomer(ctx, evt.CustomerID); t != nil {
			tenantID = t.ID
		}
	}
	if tenantID == "" {
		return errors.New("could not resolve tenant for event")
	}

	switch evt.Type {
	case "checkout.session.completed":
		if err := h.tenants.SetStripeIDs(ctx, tenantID, evt.CustomerID, evt.SubscriptionID); err != nil {
			return err
		}
		if evt.Plan != "" {
			if err := h.tenants.UpdatePlan(ctx, tenantID, evt.Plan); err != nil {
				return err
			}
		}
		return h.tenants.SetStatus(ctx, tenantID, "active")

	case "customer.subscription.updated":
		if evt.Plan != "" {
			if err := h.tenants.UpdatePlan(ctx, tenantID, evt.Plan); err != nil {
				return err
			}
		}
		// A lapsed/unpaid subscription suspends the tenant; otherwise it's active.
		status := "active"
		if evt.SubStatus == "past_due" || evt.SubStatus == "unpaid" || evt.SubStatus == "incomplete_expired" {
			status = "suspended"
		}
		return h.tenants.SetStatus(ctx, tenantID, status)

	case "customer.subscription.deleted":
		// Subscription cancelled → drop to the free plan, keep the tenant active.
		if err := h.tenants.UpdatePlan(ctx, tenantID, "free"); err != nil {
			return err
		}
		return h.tenants.SetStatus(ctx, tenantID, "active")

	default:
		return nil // ignore unhandled event types
	}
}

func (h *Handler) writeProviderError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrManualBilling) {
		http.Error(w, err.Error(), http.StatusNotImplemented)
		return
	}
	log.Printf("billing: provider error: %v", err)
	http.Error(w, "billing provider error", http.StatusBadGateway)
}

func validPlan(p string) bool {
	switch p {
	case "free", "standard", "premium", "enterprise":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
