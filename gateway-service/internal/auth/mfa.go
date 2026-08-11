package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// MFA (F18): TOTP-based two-factor auth for dashboard users.
//
// Enrolment is a two-step commit: /enroll issues a secret (stored encrypted,
// but mfa_enabled stays false) and /verify turns it on only after the user
// proves they can generate a valid code — so a half-finished enrolment can
// never lock anyone out. Login then becomes two-step for MFA users: password →
// short-lived challenge → TOTP or single-use recovery code → session.

const (
	mfaIssuer            = "PulseTrace"
	mfaChallengeTTL      = 5 * time.Minute
	recoveryCodeCount    = 10
	mfaRecoveryCodeBytes = 5 // 10 hex chars, shown as xxxxx-xxxxx
)

// ── Secret encryption at rest (AES-256-GCM) ────────────────────────────────
//
// Mirrors the notification-service channel-secret posture: the TOTP secret is
// encrypted with a key from MFA_ENCRYPTION_KEY and we fail closed if it is
// unset, rather than persisting shared secrets in the clear.

var (
	mfaAEADOnce sync.Once
	mfaAEAD     cipher.AEAD
	mfaAEADErr  error
)

// ErrNoMFAKey is returned when MFA is exercised but no encryption key is set.
var ErrNoMFAKey = errors.New("MFA_ENCRYPTION_KEY is not set — cannot store MFA secrets securely")

func mfaCipher() (cipher.AEAD, error) {
	mfaAEADOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv("MFA_ENCRYPTION_KEY"))
		if raw == "" {
			mfaAEADErr = ErrNoMFAKey
			return
		}
		block, err := aes.NewCipher(mfaDecodeKey(raw))
		if err != nil {
			mfaAEADErr = err
			return
		}
		mfaAEAD, mfaAEADErr = cipher.NewGCM(block)
	})
	return mfaAEAD, mfaAEADErr
}

// mfaDecodeKey accepts a 32-byte key as hex-64 or base64, else derives one from
// the passphrase via SHA-256 so any operator-supplied value yields a valid key.
func mfaDecodeKey(raw string) []byte {
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
		return b
	}
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func encryptSecret(plaintext string) (string, error) {
	aead, err := mfaCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func decryptSecret(encoded string) (string, error) {
	aead, err := mfaCipher()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// ── MFA challenge token ────────────────────────────────────────────────────

// issueMFAChallengeToken mints the short-lived, single-purpose token returned
// after a correct password when MFA is enabled. It carries mfa_pending:true and
// no role/tenant, and AuthMiddleware refuses it for any protected route.
func issueMFAChallengeToken(username string) (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         username,
		"exp":         jwt.NewNumericDate(now.Add(mfaChallengeTTL)),
		"iat":         jwt.NewNumericDate(now),
		"mfa_pending": true,
	})
	return token.SignedString(jwtSecret)
}

func parseMFAChallengeToken(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return "", errors.New("invalid or expired MFA challenge")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || claims["mfa_pending"] != true {
		return "", errors.New("not an MFA challenge token")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", errors.New("MFA challenge missing subject")
	}
	return sub, nil
}

// ── Recovery codes ─────────────────────────────────────────────────────────

func generateRecoveryCodes() ([]string, error) {
	codes := make([]string, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		buf := make([]byte, mfaRecoveryCodeBytes)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		h := hex.EncodeToString(buf) // 10 hex chars
		codes = append(codes, h[:5]+"-"+h[5:])
	}
	return codes, nil
}

func hashRecoveryCodes(codes []string) ([]string, error) {
	hashes := make([]string, 0, len(codes))
	for _, c := range codes {
		hash, err := bcrypt.GenerateFromPassword([]byte(normalizeRecoveryCode(c)), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, string(hash))
	}
	return hashes, nil
}

func normalizeRecoveryCode(c string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(c, "-", "")))
}

// ── Handler ────────────────────────────────────────────────────────────────

// MFAHandler serves the enrolment, verification, disable, status, and
// second-factor-login endpoints. It shares the users table with AuthHandler.
type MFAHandler struct {
	db *sql.DB
}

func NewMFAHandler(db *sql.DB) *MFAHandler {
	return &MFAHandler{db: db}
}

func mfaJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Status handles GET /api/v1/auth/mfa/status — whether the caller has MFA on.
func (h *MFAHandler) Status(w http.ResponseWriter, r *http.Request) {
	user := actorFromRequest(r)
	if user == "" {
		mfaJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var enabled bool
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COALESCE(mfa_enabled, false) FROM users WHERE username = $1", user).Scan(&enabled); err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read MFA status"})
		return
	}
	mfaJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

// Enroll handles POST /api/v1/auth/mfa/enroll — generates a secret and returns
// the provisioning URI. MFA is not yet active; the user must confirm via Verify.
func (h *MFAHandler) Enroll(w http.ResponseWriter, r *http.Request) {
	user := actorFromRequest(r)
	if user == "" {
		mfaJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	var enabled bool
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COALESCE(mfa_enabled, false) FROM users WHERE username = $1", user).Scan(&enabled); err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read user"})
		return
	}
	if enabled {
		mfaJSON(w, http.StatusConflict, map[string]string{"error": "MFA is already enabled; disable it first to re-enrol"})
		return
	}

	secret, err := GenerateTOTPSecret()
	if err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate secret"})
		return
	}
	enc, err := encryptSecret(secret)
	if err != nil {
		// Fail closed on a missing key rather than storing the secret in clear.
		log.Printf("mfa: cannot encrypt secret: %v", err)
		mfaJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "MFA is not configured on this server"})
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET mfa_secret = $1, mfa_enabled = false, mfa_recovery_codes = NULL WHERE username = $2", enc, user); err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to store secret"})
		return
	}

	mfaJSON(w, http.StatusOK, map[string]string{
		"secret":      secret,
		"otpauth_url": OTPAuthURL(mfaIssuer, user, secret),
	})
}

// Verify handles POST /api/v1/auth/mfa/verify {code} — confirms enrolment by
// checking a code against the pending secret, then activates MFA and returns
// one-time recovery codes (shown to the user exactly once).
func (h *MFAHandler) Verify(w http.ResponseWriter, r *http.Request) {
	user := actorFromRequest(r)
	if user == "" {
		mfaJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		mfaJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	var enc sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT mfa_secret FROM users WHERE username = $1", user).Scan(&enc); err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read secret"})
		return
	}
	if !enc.Valid || enc.String == "" {
		mfaJSON(w, http.StatusBadRequest, map[string]string{"error": "no enrolment in progress; call enroll first"})
		return
	}
	secret, err := decryptSecret(enc.String)
	if err != nil {
		mfaJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "MFA is not configured on this server"})
		return
	}
	if !ValidateTOTP(secret, body.Code, time.Now()) {
		mfaJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect code"})
		return
	}

	codes, err := generateRecoveryCodes()
	if err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate recovery codes"})
		return
	}
	hashes, err := hashRecoveryCodes(codes)
	if err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to secure recovery codes"})
		return
	}
	hashesJSON, _ := json.Marshal(hashes)
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET mfa_enabled = true, mfa_recovery_codes = $1 WHERE username = $2", hashesJSON, user); err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to enable MFA"})
		return
	}

	WriteAudit(h.db, user, "update", "user_mfa", user, nil, map[string]string{"mfa": "enabled"})
	mfaJSON(w, http.StatusOK, map[string]interface{}{"enabled": true, "recovery_codes": codes})
}

// Disable handles POST /api/v1/auth/mfa/disable {code} — turns MFA off after
// re-proving possession of a current TOTP or recovery code.
func (h *MFAHandler) Disable(w http.ResponseWriter, r *http.Request) {
	user := actorFromRequest(r)
	if user == "" {
		mfaJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		mfaJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	secret, recovery, enabled, err := h.loadMFA(user)
	if err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read MFA state"})
		return
	}
	if !enabled {
		mfaJSON(w, http.StatusBadRequest, map[string]string{"error": "MFA is not enabled"})
		return
	}
	if !h.consumeSecondFactor(user, secret, recovery, body.Code) {
		mfaJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect code"})
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET mfa_enabled = false, mfa_secret = NULL, mfa_recovery_codes = NULL WHERE username = $1", user); err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to disable MFA"})
		return
	}
	WriteAudit(h.db, user, "update", "user_mfa", user, nil, map[string]string{"mfa": "disabled"})
	mfaJSON(w, http.StatusOK, map[string]bool{"enabled": false})
}

// Login handles POST /api/v1/auth/mfa/login {mfa_token, code} — the second
// factor step. It verifies the challenge, checks a TOTP or recovery code, and
// only then issues the real session token.
func (h *MFAHandler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MFAToken string `json:"mfa_token"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		mfaJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	user, err := parseMFAChallengeToken(body.MFAToken)
	if err != nil {
		mfaJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	var role, tenantID, tier string
	var enabled bool
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT role, tenant_id, tier, COALESCE(mfa_enabled, false) FROM users WHERE username = $1", user).
		Scan(&role, &tenantID, &tier, &enabled); err != nil {
		mfaJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired MFA challenge"})
		return
	}
	if !enabled {
		mfaJSON(w, http.StatusBadRequest, map[string]string{"error": "MFA is not enabled for this account"})
		return
	}

	secret, recovery, _, err := h.loadMFA(user)
	if err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read MFA state"})
		return
	}
	if !h.consumeSecondFactor(user, secret, recovery, body.Code) {
		mfaJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect code"})
		return
	}

	tokenString, err := issueSessionToken(user, role, tenantID, tier)
	if err != nil {
		mfaJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}
	mfaJSON(w, http.StatusOK, TokenResponse{Token: tokenString, Role: role})
}

// loadMFA reads the decrypted secret and recovery-code hashes for a user.
func (h *MFAHandler) loadMFA(user string) (secret string, recovery []string, enabled bool, err error) {
	var enc sql.NullString
	var recoveryJSON sql.NullString
	if err = h.db.QueryRow(
		"SELECT COALESCE(mfa_enabled,false), mfa_secret, mfa_recovery_codes FROM users WHERE username = $1", user).
		Scan(&enabled, &enc, &recoveryJSON); err != nil {
		return "", nil, false, err
	}
	if enc.Valid && enc.String != "" {
		if s, derr := decryptSecret(enc.String); derr == nil {
			secret = s
		}
	}
	if recoveryJSON.Valid && recoveryJSON.String != "" {
		_ = json.Unmarshal([]byte(recoveryJSON.String), &recovery)
	}
	return secret, recovery, enabled, nil
}

// consumeSecondFactor accepts either a valid current TOTP code or an unused
// recovery code. A matched recovery code is burned (removed from the stored set)
// so it can't be replayed.
func (h *MFAHandler) consumeSecondFactor(user, secret string, recovery []string, code string) bool {
	if secret != "" && ValidateTOTP(secret, code, time.Now()) {
		return true
	}
	// Try recovery codes (single-use).
	norm := normalizeRecoveryCode(code)
	if norm == "" {
		return false
	}
	for i, hash := range recovery {
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(norm)) == nil {
			remaining := append(append([]string{}, recovery[:i]...), recovery[i+1:]...)
			remJSON, _ := json.Marshal(remaining)
			if _, err := h.db.Exec("UPDATE users SET mfa_recovery_codes = $1 WHERE username = $2", remJSON, user); err != nil {
				log.Printf("mfa: failed to burn recovery code for %s: %v", user, err)
				return false
			}
			return true
		}
	}
	return false
}
