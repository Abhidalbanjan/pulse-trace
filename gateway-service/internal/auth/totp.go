package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// RFC 6238 TOTP, implemented directly rather than pulling a dependency: it is a
// small, well-specified algorithm (HMAC-SHA1 over a 30-second counter, dynamic
// truncation to 6 digits) and keeping it in-tree means it is fully unit-tested
// against the RFC's own test vectors.

const (
	totpPeriod = 30 * time.Second
	totpDigits = 6
	// totpSecretBytes is the entropy of a freshly generated secret. 20 bytes
	// (160 bits) matches the RFC 6238 reference and what authenticator apps
	// expect; base32-encoded it is 32 characters.
	totpSecretBytes = 20
)

// GenerateTOTPSecret returns a new random base32-encoded secret (unpadded,
// uppercase) suitable for provisioning into an authenticator app.
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, totpSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// decodeSecret parses a base32 secret tolerantly: authenticator apps and users
// paste them with spaces, lower-case, and inconsistent padding.
func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(secret, " ", ""))
	s = strings.TrimRight(s, "=")
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
}

// totpAt computes the TOTP code for a secret at a specific time. Exposed
// (lower-case) for tests to pin a timestamp; production calls go through
// ValidateTOTP.
func totpAt(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", fmt.Errorf("invalid TOTP secret: %w", err)
	}
	counter := uint64(t.Unix()) / uint64(totpPeriod.Seconds())
	return hotp(key, counter), nil
}

func hotp(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	// Dynamic truncation (RFC 4226 §5.3).
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, code%mod)
}

// ValidateTOTP reports whether code is valid for secret at time now, tolerating
// ±1 time step (±30s) of clock skew between the server and the user's device.
// The comparison is constant-time to avoid leaking how many leading digits
// matched.
func ValidateTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	for skew := -1; skew <= 1; skew++ {
		want, err := totpAt(secret, now.Add(time.Duration(skew)*totpPeriod))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// OTPAuthURL builds the otpauth:// provisioning URI an authenticator app reads
// from a QR code. issuer and account are shown to the user in the app.
func OTPAuthURL(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", int(totpPeriod.Seconds())))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
