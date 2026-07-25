package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"
)

func signStripe(payload []byte, ts int64, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.%s", ts, payload)))
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func TestVerifyAndParseWebhook(t *testing.T) {
	secret := "whsec_test"
	sp := &StripeProvider{webhookSecret: secret}

	payload := []byte(`{"type":"checkout.session.completed","data":{"object":{"id":"cs_1","customer":"cus_1","subscription":"sub_1","client_reference_id":"acme","metadata":{"plan":"standard","tenant_id":"acme"}}}}`)
	sig := signStripe(payload, time.Now().Unix(), secret)

	evt, err := sp.VerifyAndParseWebhook(payload, sig)
	if err != nil {
		t.Fatalf("valid webhook rejected: %v", err)
	}
	if evt.Type != "checkout.session.completed" || evt.TenantID != "acme" || evt.CustomerID != "cus_1" ||
		evt.SubscriptionID != "sub_1" || evt.Plan != "standard" {
		t.Errorf("parsed event wrong: %+v", evt)
	}
}

func TestVerifyRejectsBadSignature(t *testing.T) {
	sp := &StripeProvider{webhookSecret: "whsec_test"}
	payload := []byte(`{"type":"x"}`)
	// Signed with a DIFFERENT secret → must be rejected.
	bad := signStripe(payload, time.Now().Unix(), "whsec_wrong")
	if _, err := sp.VerifyAndParseWebhook(payload, bad); err == nil {
		t.Error("expected rejection of a signature made with the wrong secret")
	}
}

func TestVerifyRejectsStaleTimestamp(t *testing.T) {
	secret := "whsec_test"
	sp := &StripeProvider{webhookSecret: secret}
	payload := []byte(`{"type":"x"}`)
	stale := signStripe(payload, time.Now().Add(-10*time.Minute).Unix(), secret)
	if _, err := sp.VerifyAndParseWebhook(payload, stale); err == nil {
		t.Error("expected rejection of a stale (replayed) timestamp")
	}
}

func TestVerifyRequiresWebhookSecret(t *testing.T) {
	sp := &StripeProvider{} // no webhook secret configured
	if _, err := sp.VerifyAndParseWebhook([]byte(`{}`), "t=1,v1=x"); err == nil {
		t.Error("expected error when STRIPE_WEBHOOK_SECRET is unconfigured")
	}
}

func TestManualProviderBlocksSelfServe(t *testing.T) {
	var p Provider = ManualProvider{}
	if _, err := p.Checkout(context.Background(), "acme", "", "a@b.com", "standard"); !errors.Is(err, ErrManualBilling) {
		t.Errorf("manual Checkout should return ErrManualBilling, got %v", err)
	}
	if _, err := p.Portal(context.Background(), "cus_1", "http://x"); !errors.Is(err, ErrManualBilling) {
		t.Errorf("manual Portal should return ErrManualBilling, got %v", err)
	}
}
