package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	minPasswordLength = 8
	resetTokenTTL     = time.Hour
	resetSecretBytes  = 32
)

// PasswordHandler serves authenticated password change and the forgot/reset
// flow. Password changes and completed resets revoke sessions (others / all
// respectively) so a compromised session can't outlive the credential change.
type PasswordHandler struct {
	db       *sql.DB
	sessions *SessionStore
	mailer   *Mailer
}

func NewPasswordHandler(db *sql.DB, sessions *SessionStore, mailer *Mailer) *PasswordHandler {
	return &PasswordHandler{db: db, sessions: sessions, mailer: mailer}
}

// validatePasswordPolicy is the single place password strength is enforced, so
// change/reset/signup can't drift apart. Deliberately minimal (length only) —
// enough to reject the obviously weak without inventing composition rules that
// push users toward predictable patterns.
func validatePasswordPolicy(pw string) error {
	if len(pw) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	return nil
}

// Change handles POST /api/v1/auth/password/change {current_password,
// new_password} for an authenticated user, then revokes their other sessions.
func (h *PasswordHandler) Change(w http.ResponseWriter, r *http.Request) {
	user := actorFromRequest(r)
	if user == "" {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := validatePasswordPolicy(body.NewPassword); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var storedHash string
	if err := h.db.QueryRowContext(r.Context(), "SELECT password_hash FROM users WHERE username = $1", user).Scan(&storedHash); err != nil {
		http.Error(w, "user not found", http.StatusUnauthorized)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(body.CurrentPassword)) != nil {
		http.Error(w, "current password is incorrect", http.StatusUnauthorized)
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "failed to hash password", http.StatusInternalServerError)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), "UPDATE users SET password_hash = $1 WHERE username = $2", string(newHash), user); err != nil {
		http.Error(w, "failed to update password", http.StatusInternalServerError)
		return
	}

	// Keep the acting device signed in, sign out the rest.
	if err := h.sessions.RevokeOthersForUser(r.Context(), user, currentSession(r)); err != nil {
		log.Printf("password: change revoke-others failed for %s: %v", user, err)
	}
	WriteAudit(h.db, user, "update", "user_password", user, nil, map[string]string{"action": "changed"})
	writeSessionJSON(w, map[string]bool{"changed": true})
}

// Forgot handles POST /api/v1/auth/password/forgot {username}. It always
// responds 200 with the same body whether or not the account exists — never
// leaking which usernames are real — and, when it is real, creates a single-use
// reset token and delivers a link.
func (h *PasswordHandler) Forgot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(body.Username)

	// Uniform response regardless of outcome (anti-enumeration).
	respond := func() {
		writeSessionJSON(w, map[string]string{"status": "if that account exists, a reset link has been sent"})
	}

	if username == "" {
		respond()
		return
	}

	var exists bool
	if err := h.db.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM users WHERE username = $1)", username).Scan(&exists); err != nil || !exists {
		respond()
		return
	}

	token, err := h.createResetToken(r.Context(), username)
	if err != nil {
		log.Printf("password: failed to create reset token for %s: %v", username, err)
		respond()
		return
	}

	link := resetLink(token)
	if h.mailer.Configured() {
		body := fmt.Sprintf("A password reset was requested for your PulseTrace account.\n\nReset your password:\n%s\n\nThis link expires in 1 hour. If you didn't request it, you can ignore this email.", link)
		if err := h.mailer.Send(username, "Reset your PulseTrace password", body); err != nil {
			log.Printf("password: failed to send reset email to %s: %v", username, err)
		}
	} else {
		// Dev/on-prem without SMTP: the operator can read the link from logs.
		// Never returned in the HTTP response, so this can't leak to a caller.
		log.Printf("password: SMTP not configured; reset link for %s: %s", username, link)
	}
	respond()
}

// Reset handles POST /api/v1/auth/password/reset {token, new_password}. It
// verifies the single-use token, sets the new password, burns the token, and
// revokes ALL of the user's sessions (a reset means "I lost control, start
// clean").
func (h *PasswordHandler) Reset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := validatePasswordPolicy(body.NewPassword); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	username, err := h.consumeResetToken(r.Context(), body.Token)
	if err != nil {
		http.Error(w, "invalid or expired reset token", http.StatusBadRequest)
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "failed to hash password", http.StatusInternalServerError)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), "UPDATE users SET password_hash = $1 WHERE username = $2", string(newHash), username); err != nil {
		http.Error(w, "failed to update password", http.StatusInternalServerError)
		return
	}
	if err := h.sessions.RevokeAllForUser(r.Context(), username); err != nil {
		log.Printf("password: reset revoke-all failed for %s: %v", username, err)
	}
	WriteAudit(h.db, username, "update", "user_password", username, nil, map[string]string{"action": "reset"})
	writeSessionJSON(w, map[string]bool{"reset": true})
}

// ── Reset token internals ──────────────────────────────────────────────────

// A reset token is "<id>.<secret>": the id locates the row (so verification is
// an O(1) lookup, not a scan), and only bcrypt(secret) is stored, so the token
// can't be reconstructed from the database.

func (h *PasswordHandler) createResetToken(ctx context.Context, username string) (string, error) {
	id := uuid.NewString()
	secretBytes := make([]byte, resetSecretBytes)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(secretBytes)
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	if _, err := h.db.ExecContext(ctx,
		"INSERT INTO password_resets (id, username, token_hash, expires_at) VALUES ($1, $2, $3, $4)",
		id, username, string(hash), time.Now().Add(resetTokenTTL)); err != nil {
		return "", err
	}
	return id + "." + secret, nil
}

func (h *PasswordHandler) consumeResetToken(ctx context.Context, token string) (string, error) {
	id, secret, ok := splitResetToken(token)
	if !ok {
		return "", errors.New("malformed token")
	}
	var username, hash string
	err := h.db.QueryRowContext(ctx,
		"SELECT username, token_hash FROM password_resets WHERE id = $1 AND used_at IS NULL AND expires_at > now()",
		id).Scan(&username, &hash)
	if err != nil {
		return "", errors.New("token not found or expired")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)) != nil {
		return "", errors.New("token mismatch")
	}
	// Burn it atomically: only the row that is still unused flips, so a
	// double-submit can't reset twice.
	res, err := h.db.ExecContext(ctx, "UPDATE password_resets SET used_at = now() WHERE id = $1 AND used_at IS NULL", id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", errors.New("token already used")
	}
	return username, nil
}

// splitResetToken parses "<id>.<secret>" with a constant-time-friendly shape.
func splitResetToken(token string) (id, secret string, ok bool) {
	token = strings.TrimSpace(token)
	i := strings.IndexByte(token, '.')
	if i <= 0 || i == len(token)-1 {
		return "", "", false
	}
	id, secret = token[:i], token[i+1:]
	// Guard against absurd inputs so a malformed token can't blow past bcrypt.
	if len(secret) == 0 || len(secret) > 256 || subtle.ConstantTimeEq(int32(len(id)), 0) == 1 {
		return "", "", false
	}
	return id, secret, true
}

func resetLink(token string) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("APP_BASE_URL")), "/")
	if base == "" {
		base = "http://localhost:3000"
	}
	return fmt.Sprintf("%s/reset-password?token=%s", base, token)
}
