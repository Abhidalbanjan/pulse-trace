package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Tenant is a first-class organization: a plan, a status, and (for SaaS) links
// to its billing customer/subscription.
type Tenant struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Plan                 string `json:"plan"`
	Status               string `json:"status"`
	StripeCustomerID     string `json:"stripe_customer_id,omitempty"`
	StripeSubscriptionID string `json:"stripe_subscription_id,omitempty"`
	CreatedAt            string `json:"created_at,omitempty"`
}

// TenantStore is the data-access layer for tenants, plus the self-serve signup
// flow (which creates a tenant and its first admin atomically).
type TenantStore struct {
	db *sql.DB
}

func NewTenantStore(db *sql.DB) *TenantStore {
	return &TenantStore{db: db}
}

// GetTenant returns a tenant by id, or (nil, nil) if it doesn't exist.
func (s *TenantStore) GetTenant(ctx context.Context, id string) (*Tenant, error) {
	var t Tenant
	var cust, sub sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, plan, status, COALESCE(stripe_customer_id,''), COALESCE(stripe_subscription_id,''), created_at::text FROM tenants WHERE id = $1",
		id,
	).Scan(&t.ID, &t.Name, &t.Plan, &t.Status, &cust, &sub, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.StripeCustomerID = cust.String
	t.StripeSubscriptionID = sub.String
	return &t, nil
}

var slugInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns an org name into a tenant id: lowercase, non-alphanumerics
// collapsed to single hyphens, trimmed, capped at 40 chars. Empty input yields "".
func slugify(name string) string {
	s := slugInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

func randomSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SignupRequest is the self-serve signup payload: an org plus its first admin.
type SignupRequest struct {
	OrgName  string `json:"org_name"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Signup handles POST /api/v1/auth/signup. It creates a brand-new tenant on the
// free plan and its first user as that tenant's ADMIN, atomically, then returns a
// JWT so the client is logged straight in. This is the self-serve funnel — it
// never joins an existing tenant (that's what invites/registration would do).
func (s *TenantStore) Signup(w http.ResponseWriter, r *http.Request) {
	var req SignupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.OrgName = strings.TrimSpace(req.OrgName)
	if req.OrgName == "" || req.Username == "" || req.Password == "" {
		http.Error(w, "org_name, username, and password are required", http.StatusBadRequest)
		return
	}

	base := slugify(req.OrgName)
	if base == "" {
		base = "org"
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "failed to hash password", http.StatusInternalServerError)
		return
	}

	// Create the tenant + admin user in one transaction. On a slug collision we
	// retry with a random suffix a few times before giving up.
	tenantID, err := s.createTenantWithAdmin(r.Context(), base, req.OrgName, req.Username, string(hashed))
	if err != nil {
		switch {
		case errors.Is(err, errUsernameTaken):
			http.Error(w, "username already exists", http.StatusConflict)
		case errors.Is(err, errSlugExhausted):
			http.Error(w, "could not allocate a unique organization id, try a different name", http.StatusConflict)
		default:
			log.Printf("signup: failed: %v", err)
			http.Error(w, "failed to create account", http.StatusInternalServerError)
		}
		return
	}

	token, err := issueToken(req.Username, "admin", tenantID, "free")
	if err != nil {
		http.Error(w, "account created but failed to issue token; please log in", http.StatusInternalServerError)
		return
	}

	WriteAudit(s.db, req.Username, "create", "tenant", tenantID, nil,
		map[string]string{"org_name": req.OrgName, "admin": req.Username, "plan": "free"})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":    "created",
		"token":     token,
		"tenant_id": tenantID,
		"role":      "admin",
	})
}

var (
	errUsernameTaken = errors.New("username taken")
	errSlugExhausted = errors.New("slug exhausted")
)

// createTenantWithAdmin inserts the tenant and its admin user atomically,
// retrying the tenant id with a random suffix on slug collision.
func (s *TenantStore) createTenantWithAdmin(ctx context.Context, baseSlug, name, username, passwordHash string) (string, error) {
	// Pre-check username to give a clean 409 rather than a foreign-key/uniqueness
	// error surfacing from deep in the tx.
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE username = $1)", username).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "", errUsernameTaken
	}

	for attempt := 0; attempt < 5; attempt++ {
		tenantID := baseSlug
		if attempt > 0 {
			tenantID = baseSlug + "-" + randomSuffix()
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return "", err
		}

		_, err = tx.ExecContext(ctx, "INSERT INTO tenants (id, name, plan, status) VALUES ($1, $2, 'free', 'active')", tenantID, name)
		if err != nil {
			_ = tx.Rollback()
			if isUniqueViolation(err) {
				continue // slug collision — retry with a suffix
			}
			return "", err
		}

		_, err = tx.ExecContext(ctx,
			"INSERT INTO users (username, password_hash, role, tenant_id, tier) VALUES ($1, $2, 'admin', $3, 'free')",
			username, passwordHash, tenantID)
		if err != nil {
			_ = tx.Rollback()
			if isUniqueViolation(err) {
				return "", errUsernameTaken
			}
			return "", err
		}

		if err := tx.Commit(); err != nil {
			return "", err
		}
		return tenantID, nil
	}
	return "", errSlugExhausted
}

func isUniqueViolation(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key")
}

// GetCurrentTenant handles GET /api/v1/tenant — the caller's own tenant.
func (s *TenantStore) GetCurrentTenant(w http.ResponseWriter, r *http.Request) {
	t, err := s.GetTenant(r.Context(), tenantOf(r))
	if err != nil {
		http.Error(w, "failed to load tenant", http.StatusInternalServerError)
		return
	}
	if t == nil {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(t)
}

// issueToken mints a PulseTrace JWT — shared by Signup and Login so the claim
// shape stays identical.
func issueToken(subject, role, tenantID, tier string) (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         subject,
		"exp":         jwt.NewNumericDate(now.Add(24 * time.Hour)),
		"iat":         jwt.NewNumericDate(now),
		"role":        role,
		"tenant_id":   tenantID,
		"tenant_tier": tier,
	})
	return token.SignedString(jwtSecret)
}
