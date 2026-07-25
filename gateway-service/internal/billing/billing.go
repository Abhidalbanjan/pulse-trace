// Package billing abstracts subscription billing behind a provider interface so
// the same product runs as self-serve SaaS (Stripe) or enterprise on-prem
// (manual, no external calls). The provider is chosen at startup from
// BILLING_PROVIDER; everything above it (handlers, plan updates) is identical.
package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrManualBilling is returned by the manual provider for self-serve billing
// actions — on-prem plans are set by an operator, not a checkout page.
var ErrManualBilling = errors.New("self-serve billing is disabled on this deployment; plans are managed by your account team")

// Provider is the billing backend. Checkout starts/upgrades a subscription and
// returns a redirect URL; Portal returns a self-service management URL.
type Provider interface {
	Name() string
	Checkout(ctx context.Context, tenantID, customerID, email, plan string) (redirectURL string, err error)
	Portal(ctx context.Context, customerID, returnURL string) (url string, err error)
}

// FromEnv selects the provider from BILLING_PROVIDER ("stripe" | "manual",
// default "manual"). Stripe needs STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET, and
// STRIPE_PRICE_<PLAN> ids; if it's requested but unconfigured we fall back to
// manual with a warning rather than booting a half-configured billing path.
func FromEnv() Provider {
	switch strings.ToLower(os.Getenv("BILLING_PROVIDER")) {
	case "stripe":
		if key := os.Getenv("STRIPE_SECRET_KEY"); key != "" {
			return newStripeProvider(key)
		}
		fmt.Println("billing: BILLING_PROVIDER=stripe but STRIPE_SECRET_KEY is unset; using manual billing")
		return ManualProvider{}
	default:
		return ManualProvider{}
	}
}

// ── Manual provider (enterprise / on-prem) ────────────────────────────────────

type ManualProvider struct{}

func (ManualProvider) Name() string { return "manual" }
func (ManualProvider) Checkout(context.Context, string, string, string, string) (string, error) {
	return "", ErrManualBilling
}
func (ManualProvider) Portal(context.Context, string, string) (string, error) {
	return "", ErrManualBilling
}

// ── Stripe provider (SaaS) ────────────────────────────────────────────────────
//
// Implemented with raw HTTP against the Stripe API + HMAC webhook verification,
// so on-prem builds don't carry the Stripe SDK's dependency tree.

type StripeProvider struct {
	secretKey     string
	webhookSecret string
	priceForPlan  map[string]string // plan -> Stripe price id
	successURL    string
	cancelURL     string
	client        *http.Client
}

func newStripeProvider(secretKey string) *StripeProvider {
	base := envOr("PUBLIC_BASE_URL", "http://localhost:3000")
	return &StripeProvider{
		secretKey:     secretKey,
		webhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
		priceForPlan: map[string]string{
			"standard": os.Getenv("STRIPE_PRICE_STANDARD"),
			"premium":  os.Getenv("STRIPE_PRICE_PREMIUM"),
		},
		successURL: base + "/settings?billing=success",
		cancelURL:  base + "/settings?billing=cancelled",
		client:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *StripeProvider) Name() string { return "stripe" }

func (s *StripeProvider) Checkout(ctx context.Context, tenantID, customerID, email, plan string) (string, error) {
	price := s.priceForPlan[plan]
	if price == "" {
		return "", fmt.Errorf("no Stripe price configured for plan %q", plan)
	}
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("line_items[0][price]", price)
	form.Set("line_items[0][quantity]", "1")
	form.Set("success_url", s.successURL)
	form.Set("cancel_url", s.cancelURL)
	form.Set("client_reference_id", tenantID)
	// Carry tenant+plan on the subscription so later subscription.* webhooks
	// resolve back to the tenant and the intended plan.
	form.Set("subscription_data[metadata][tenant_id]", tenantID)
	form.Set("subscription_data[metadata][plan]", plan)
	form.Set("metadata[tenant_id]", tenantID)
	form.Set("metadata[plan]", plan)
	if customerID != "" {
		form.Set("customer", customerID)
	} else if email != "" {
		form.Set("customer_email", email)
	}

	var out struct {
		URL string `json:"url"`
	}
	if err := s.post(ctx, "/v1/checkout/sessions", form, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (s *StripeProvider) Portal(ctx context.Context, customerID, returnURL string) (string, error) {
	if customerID == "" {
		return "", errors.New("tenant has no Stripe customer yet; complete checkout first")
	}
	form := url.Values{}
	form.Set("customer", customerID)
	form.Set("return_url", returnURL)
	var out struct {
		URL string `json:"url"`
	}
	if err := s.post(ctx, "/v1/billing_portal/sessions", form, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (s *StripeProvider) post(ctx context.Context, path string, form url.Values, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.stripe.com"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.secretKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("stripe %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

// WebhookEvent is the normalized subset of a Stripe event the billing handler acts on.
type WebhookEvent struct {
	Type           string
	TenantID       string // from client_reference_id / metadata.tenant_id
	CustomerID     string
	SubscriptionID string
	Plan           string // from metadata.plan
	SubStatus      string // subscription status (active/past_due/canceled/...)
}

const stripeWebhookTolerance = 5 * time.Minute

// VerifyAndParseWebhook checks the Stripe-Signature HMAC and extracts the fields
// the handler needs. A missing webhook secret is a hard error — we never trust an
// unverified billing event.
func (s *StripeProvider) VerifyAndParseWebhook(payload []byte, sigHeader string) (*WebhookEvent, error) {
	if s.webhookSecret == "" {
		return nil, errors.New("STRIPE_WEBHOOK_SECRET is not configured")
	}
	if err := verifyStripeSignature(payload, sigHeader, s.webhookSecret); err != nil {
		return nil, err
	}

	var evt struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID                string            `json:"id"`
				Customer          string            `json:"customer"`
				Subscription      string            `json:"subscription"`
				Status            string            `json:"status"`
				ClientReferenceID string            `json:"client_reference_id"`
				Metadata          map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		return nil, err
	}
	obj := evt.Data.Object
	out := &WebhookEvent{
		Type:           evt.Type,
		CustomerID:     obj.Customer,
		SubscriptionID: obj.Subscription,
		SubStatus:      obj.Status,
		TenantID:       obj.ClientReferenceID,
		Plan:           obj.Metadata["plan"],
	}
	if out.TenantID == "" {
		out.TenantID = obj.Metadata["tenant_id"]
	}
	// For a subscription object the id IS the subscription id.
	if strings.HasPrefix(evt.Type, "customer.subscription.") && out.SubscriptionID == "" {
		out.SubscriptionID = obj.ID
	}
	return out, nil
}

func verifyStripeSignature(payload []byte, sigHeader, secret string) error {
	var ts string
	var sigs []string
	for _, part := range strings.Split(sigHeader, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	if ts == "" || len(sigs) == 0 {
		return errors.New("malformed Stripe-Signature header")
	}
	// Reject stale timestamps to blunt replay.
	if secs, err := parseUnix(ts); err == nil {
		if time.Since(time.Unix(secs, 0)).Abs() > stripeWebhookTolerance {
			return errors.New("Stripe webhook timestamp outside tolerance")
		}
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(expected)) {
			return nil
		}
	}
	return errors.New("Stripe signature verification failed")
}

func parseUnix(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
