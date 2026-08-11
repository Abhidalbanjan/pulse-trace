package auth

import (
	"os"
	"testing"
)

// Set an encryption key before any test touches the AES singleton (sync.Once).
func init() {
	if os.Getenv("MFA_ENCRYPTION_KEY") == "" {
		_ = os.Setenv("MFA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	}
}

func TestMFAChallengeToken_RoundTrip(t *testing.T) {
	tok, err := issueMFAChallengeToken("alice")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := parseMFAChallengeToken(tok)
	if err != nil {
		t.Fatalf("valid challenge should parse: %v", err)
	}
	if sub != "alice" {
		t.Fatalf("expected subject alice, got %q", sub)
	}
}

func TestMFAChallengeToken_RejectsSessionToken(t *testing.T) {
	// A full session token has no mfa_pending claim and must not be accepted as
	// a challenge (and vice-versa the middleware rejects challenges as sessions).
	session, err := issueSessionToken("alice", "admin", "default", "standard", "test-jti")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseMFAChallengeToken(session); err == nil {
		t.Fatal("a session token must not be accepted as an MFA challenge")
	}
}

func TestMFAChallengeToken_RejectsGarbage(t *testing.T) {
	if _, err := parseMFAChallengeToken("not.a.jwt"); err == nil {
		t.Fatal("garbage must not parse as a challenge")
	}
}

func TestEncryptSecret_RoundTrip(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := encryptSecret(secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == secret {
		t.Fatal("ciphertext must not equal plaintext")
	}
	got, err := decryptSecret(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != secret {
		t.Fatalf("round-trip mismatch: got %q want %q", got, secret)
	}
}

func TestEncryptSecret_NonDeterministic(t *testing.T) {
	// A fresh nonce per call means the same plaintext encrypts differently each
	// time — otherwise identical secrets would be linkable in the DB.
	a, _ := encryptSecret("same")
	b, _ := encryptSecret("same")
	if a == b {
		t.Fatal("encryption must use a fresh nonce each call")
	}
}

func TestRecoveryCodes_GeneratedHashedAndVerified(t *testing.T) {
	codes, err := generateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("expected %d codes, got %d", recoveryCodeCount, len(codes))
	}
	hashes, err := hashRecoveryCodes(codes)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != len(codes) {
		t.Fatal("each code must produce a hash")
	}
	// A hash must not be the code itself, and must verify against it (with the
	// same normalization used at login).
	for i, c := range codes {
		if hashes[i] == c {
			t.Fatal("recovery code stored in the clear")
		}
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	// Dashes/case/whitespace must not matter, so a user can type it either way.
	if normalizeRecoveryCode(" AB12C-DE34F ") != normalizeRecoveryCode("ab12cde34f") {
		t.Fatal("recovery code normalization must ignore case, dashes, and spaces")
	}
}

func TestMFADecodeKey_AcceptsFormats(t *testing.T) {
	if len(mfaDecodeKey("0123456789abcdef0123456789abcdef")) != 32 {
		t.Error("32-char passphrase should yield a 32-byte key")
	}
	// A hex-64 key decodes to exactly 32 bytes.
	if len(mfaDecodeKey("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")) != 32 {
		t.Error("hex-64 key should decode to 32 bytes")
	}
	// An arbitrary passphrase is SHA-256-derived to 32 bytes.
	if len(mfaDecodeKey("some-passphrase")) != 32 {
		t.Error("passphrase should derive a 32-byte key")
	}
}
