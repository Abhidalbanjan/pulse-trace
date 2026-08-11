package auth

import (
	"strings"
	"testing"
)

func TestValidatePasswordPolicy(t *testing.T) {
	if err := validatePasswordPolicy("short"); err == nil {
		t.Error("a too-short password must be rejected")
	}
	if err := validatePasswordPolicy("longenough1"); err != nil {
		t.Errorf("an 8+ char password must pass, got: %v", err)
	}
}

func TestSplitResetToken(t *testing.T) {
	id, secret, ok := splitResetToken("abc-123.deadbeef")
	if !ok || id != "abc-123" || secret != "deadbeef" {
		t.Fatalf("expected clean split, got id=%q secret=%q ok=%v", id, secret, ok)
	}
	bad := []string{"", "noseparator", ".onlysecret", "onlyid.", "  "}
	for _, tok := range bad {
		if _, _, ok := splitResetToken(tok); ok {
			t.Errorf("expected %q to be rejected", tok)
		}
	}
	// An over-long secret is rejected so it can't blow past bcrypt's input cap.
	if _, _, ok := splitResetToken("id." + strings.Repeat("a", 300)); ok {
		t.Error("an absurdly long secret must be rejected")
	}
}

func TestResetLink_UsesBaseURL(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://app.example.com/")
	link := resetLink("tok123")
	if link != "https://app.example.com/reset-password?token=tok123" {
		t.Fatalf("unexpected reset link: %s", link)
	}
}

func TestBuildMessage_WellFormed(t *testing.T) {
	msg := string(buildMessage("from@x.com", "to@y.com", "Hi", "body here"))
	for _, want := range []string{"From: from@x.com\r\n", "To: to@y.com\r\n", "Subject: Hi\r\n", "\r\n\r\nbody here"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestMailer_UnconfiguredReportsFalse(t *testing.T) {
	t.Setenv("SMTP_HOST", "")
	m := MailerFromEnv()
	if m.Configured() {
		t.Fatal("mailer with no SMTP_HOST must report unconfigured")
	}
	if err := m.Send("a@b.com", "s", "b"); err == nil {
		t.Fatal("sending via an unconfigured mailer must error")
	}
}
