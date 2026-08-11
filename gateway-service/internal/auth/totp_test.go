package auth

import (
	"testing"
	"time"
)

// rfc6238Secret is the RFC 6238 test seed "12345678901234567890" (ASCII) in
// base32 — the canonical vector for SHA1 TOTP.
const rfc6238Secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestTOTP_RFC6238Vectors(t *testing.T) {
	// The RFC publishes 8-digit codes; PulseTrace uses 6, so we assert the low
	// 6 digits of each published vector.
	cases := []struct {
		unix int64
		want string // last 6 digits of the RFC's 8-digit code
	}{
		{59, "287082"},         // RFC: 94287082
		{1111111109, "081804"}, // RFC: 07081804
		{1111111111, "050471"}, // RFC: 14050471
		{1234567890, "005924"}, // RFC: 89005924
		{2000000000, "279037"}, // RFC: 69279037
	}
	for _, c := range cases {
		got, err := totpAt(rfc6238Secret, time.Unix(c.unix, 0).UTC())
		if err != nil {
			t.Fatalf("totpAt(%d): %v", c.unix, err)
		}
		if got != c.want {
			t.Errorf("totpAt(%d) = %s, want %s", c.unix, got, c.want)
		}
	}
}

func TestValidateTOTP_AcceptsCurrentCode(t *testing.T) {
	now := time.Now().UTC()
	code, err := totpAt(rfc6238Secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidateTOTP(rfc6238Secret, code, now) {
		t.Fatal("current code should validate")
	}
}

func TestValidateTOTP_ToleratesClockSkew(t *testing.T) {
	now := time.Now().UTC()
	// A code generated one step ago (device clock behind) must still pass.
	prev, _ := totpAt(rfc6238Secret, now.Add(-totpPeriod))
	if !ValidateTOTP(rfc6238Secret, prev, now) {
		t.Error("code from previous step should validate (−1 skew)")
	}
	next, _ := totpAt(rfc6238Secret, now.Add(totpPeriod))
	if !ValidateTOTP(rfc6238Secret, next, now) {
		t.Error("code from next step should validate (+1 skew)")
	}
}

func TestValidateTOTP_RejectsStaleAndWrongCodes(t *testing.T) {
	now := time.Now().UTC()
	// Two steps in the past is outside the ±1 window.
	old, _ := totpAt(rfc6238Secret, now.Add(-2*totpPeriod))
	if ValidateTOTP(rfc6238Secret, old, now) {
		t.Error("a code two steps old must be rejected")
	}
	if ValidateTOTP(rfc6238Secret, "000000", now) && func() bool {
		c, _ := totpAt(rfc6238Secret, now)
		return c != "000000"
	}() {
		t.Error("an arbitrary wrong code must be rejected")
	}
	if ValidateTOTP(rfc6238Secret, "12345", now) {
		t.Error("a malformed (wrong length) code must be rejected")
	}
}

func TestGenerateTOTPSecret_UsableRoundTrip(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) == 0 {
		t.Fatal("empty secret")
	}
	now := time.Now().UTC()
	code, err := totpAt(secret, now)
	if err != nil {
		t.Fatalf("generated secret must be decodable: %v", err)
	}
	if !ValidateTOTP(secret, code, now) {
		t.Fatal("a freshly generated secret must produce validatable codes")
	}
}

func TestDecodeSecret_TolerantOfFormatting(t *testing.T) {
	// Same secret with lowercase + spaces (how a user might paste it).
	if _, err := decodeSecret("gezd gnbv gy3t qojq gezd gnbv gy3t qojq"); err != nil {
		t.Fatalf("should tolerate spaced/lowercase secret: %v", err)
	}
}

func TestOTPAuthURL_WellFormed(t *testing.T) {
	u := OTPAuthURL("PulseTrace", "alice", rfc6238Secret)
	for _, want := range []string{"otpauth://totp/", "secret=" + rfc6238Secret, "issuer=PulseTrace", "digits=6", "period=30"} {
		if !contains(u, want) {
			t.Errorf("otpauth URL missing %q: %s", want, u)
		}
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (func() bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
})() }
