package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var jwtSecret = loadJWTSecret()

// loadJWTSecret requires JWT_SECRET in production. If it isn't set, we do NOT
// fall back to a guessable hardcoded string (that string would be visible to
// anyone who reads this public repo, letting them forge admin tokens against
// any deployment that forgot to override it). Instead we generate a random
// per-process secret so local dev still works out of the box — the tradeoff
// is that every process restart invalidates existing sessions, which is a
// strong signal to set JWT_SECRET before this ever runs somewhere real.
func loadJWTSecret() []byte {
	if v := os.Getenv("JWT_SECRET"); v != "" {
		return []byte(v)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		log.Fatalf("auth: failed to generate fallback JWT secret: %v", err)
	}
	log.Println("auth: WARNING — JWT_SECRET is not set. Using a random per-process secret; " +
		"all sessions will be invalidated on restart. Set JWT_SECRET before deploying anywhere real.")
	return secret
}

type AuthHandler struct {
	db *sql.DB
}

func NewAuthHandler() (*AuthHandler, error) {
	connStr := getEnv("DATABASE_URL", "postgres://pulsetrace:pulsetrace_secret@localhost:5434/pulsetrace?sslmode=disable")
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	// Ping database to ensure connection
	if err := db.Ping(); err != nil {
		log.Printf("auth: warning, failed to ping database: %v", err)
	} else {
		log.Println("auth: connected to postgres for authentication")
	}

	return &AuthHandler{db: db}, nil
}

func (h *AuthHandler) GetDB() *sql.DB {
	return h.db
}

// roleExists checks the dynamic roles table (see rbac.go) rather than a
// hardcoded admin/viewer list, so newly-created custom roles are assignable.
func (h *AuthHandler) roleExists(role string) bool {
	var exists bool
	err := h.db.QueryRow("SELECT EXISTS(SELECT 1 FROM roles WHERE name = $1)", role).Scan(&exists)
	if err != nil {
		log.Printf("auth: failed to check role existence: %v", err)
		return false
	}
	return exists
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type TokenResponse struct {
	Token string `json:"token"`
	Role  string `json:"role"`

	// MFARequired + MFAToken are set instead of Token when the account has MFA
	// enabled: the password was correct, but a second factor is still required.
	// The client exchanges MFAToken plus a TOTP/recovery code at
	// POST /api/v1/auth/mfa/login for the real session token. No role or session
	// JWT is issued until that second step completes.
	MFARequired bool   `json:"mfa_required,omitempty"`
	MFAToken    string `json:"mfa_token,omitempty"`
}

// sessionTokenTTL is how long an issued dashboard session is valid.
const sessionTokenTTL = 24 * time.Hour

// issueSessionToken mints the signed JWT that authorizes dashboard/API access.
// Centralized so the password path, the MFA second-factor path, and any future
// login path all produce identically-shaped, identically-signed sessions. The
// jti binds the token to a revocable user_sessions row (see SessionStore).
func issueSessionToken(username, role, tenantID, tier, jti string) (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         username,
		"exp":         jwt.NewNumericDate(now.Add(sessionTokenTTL)),
		"iat":         jwt.NewNumericDate(now),
		"jti":         jti,
		"role":        role,
		"tenant_id":   tenantID,
		"tenant_tier": tier,
	})
	return token.SignedString(jwtSecret)
}

// clientIP extracts the best-effort caller IP for session bookkeeping, honoring
// the gateway's own X-Forwarded-For when present.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}

// Login handles POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var creds Credentials
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var storedHash, role, tenantID, tier string
	var mfaEnabled bool
	err := h.db.QueryRow("SELECT password_hash, role, tenant_id, tier, COALESCE(mfa_enabled, false) FROM users WHERE username = $1", creds.Username).
		Scan(&storedHash, &role, &tenantID, &tier, &mfaEnabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Invalid username or password", http.StatusUnauthorized)
			return
		}
		log.Printf("auth error querying user: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Compare passwords
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(creds.Password)); err != nil {
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// MFA gate: with a second factor enrolled, a correct password alone does not
	// yield a session. Issue a short-lived challenge the client redeems at
	// /api/v1/auth/mfa/login with a TOTP or recovery code.
	if mfaEnabled {
		challenge, err := issueMFAChallengeToken(creds.Username)
		if err != nil {
			http.Error(w, "Failed to start MFA challenge", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(TokenResponse{MFARequired: true, MFAToken: challenge})
		return
	}

	jti := createSession(r.Context(), h.db, creds.Username, tenantID, r.UserAgent(), clientIP(r))
	tokenString, err := issueSessionToken(creds.Username, role, tenantID, tier, jti)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(TokenResponse{Token: tokenString, Role: role})
}

const (
	defaultTenantID   = "default"
	defaultTenantTier = "standard"
)

// requireIngestionKey, when true, rejects server-side telemetry ingestion that
// doesn't present a valid ingestion key instead of quietly attributing it to the
// "default" tenant. It defaults to false so a fresh local/dev stack ingests out
// of the box, but MUST be set true in any real multi-tenant deployment — the
// same posture the codebase already takes for JWT_SECRET. Read once at startup.
var requireIngestionKey = strings.EqualFold(os.Getenv("REQUIRE_INGESTION_KEY"), "true")

// RequireIngestionKey reports whether telemetry ingestion must present a valid
// ingestion key (REQUIRE_INGESTION_KEY). Exposed so the in-process OTLP/gRPC
// receiver enforces the exact same policy as the HTTP ingestion path.
func RequireIngestionKey() bool { return requireIngestionKey }

// bearerToken extracts the token from an "Authorization: Bearer <token>" header,
// or "" if absent/malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" || !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}

// AuthMiddleware authenticates requests and, crucially, resolves tenant identity
// from a server-verifiable credential rather than a client-supplied header.
//
// Two credential types are recognized:
//   - a JWT (dashboard/API users) → tenant comes from signed claims;
//   - an ingestion key (telemetry agents) → tenant comes from the ingestion_keys
//     row the key hashes to (see IngestionKeyStore).
//
// Before this, ingestion endpoints trusted an X-Tenant-ID request header
// verbatim, so any caller could write into any tenant's data. That header (and
// the other identity headers downstream services trust) is now stripped from
// every inbound request up front and only ever re-set from a verified source.
func AuthMiddleware(keys *IngestionKeyStore, sessions *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Strip client-supplied identity headers on EVERY request. Downstream
			// services trust these to mean "the gateway verified this"; a client
			// must never be able to set them itself, on any route.
			r.Header.Del("X-Tenant-ID")
			r.Header.Del("X-Tenant-Tier")
			r.Header.Del("X-User-Role")
			r.Header.Del("X-User-Subject")
			r.Header.Del("X-Session-ID")

			// Route classification.
			// NOTE: this middleware covers only the HTTP ingestion paths below. The
			// OTLP/gRPC path (:4317) is NOT a bypass — it's terminated in-process by
			// the otlp.Receiver, which does its own ingestion-key auth, scope check,
			// tenant stamping, quota and metering on every export (see
			// internal/otlp/tenant.go). It just can't reuse this HTTP middleware, so
			// the equivalent checks live there instead.
			isOTLPHTTP := strings.HasPrefix(r.URL.Path, "/v1/traces") ||
				strings.HasPrefix(r.URL.Path, "/v1/metrics") ||
				strings.HasPrefix(r.URL.Path, "/v1/logs")
			isLogIngest := r.URL.Path == "/api/v1/logs" && r.Method == http.MethodPost
			isServerIngest := isOTLPHTTP || isLogIngest
			// RUM comes from browsers and must stay reachable for anonymous visitors
			// on public pages (e.g. /login before sign-in), so it is never blocked by
			// REQUIRE_INGESTION_KEY. Per-tenant RUM attribution via a public client
			// token is a Phase 2/3 follow-up; today an un-keyed RUM event is 'default'.
			isRUMIngest := r.URL.Path == "/api/v1/rum/ingest" && r.Method == http.MethodPost
			// The agent polls agent-config (unauthenticated) for its dynamic log level;
			// it presents its ingestion key so per-service state is read from the right
			// tenant rather than always the default one.
			isAgentConfig := strings.HasPrefix(r.URL.Path, "/api/v1/topology/agent-config")

			// "Trojan Horse" migration ingestion (Datadog trace-agent /v0.x/traces,
			// Splunk HEC /services/collector). These authenticate via their own
			// protocol header (DD-API-KEY / "Authorization: Splunk <token>") inside
			// the ingestproxy handler, not via a JWT here, so they bypass the JWT
			// gate. Identity headers were already stripped above, so nothing tenant-
			// identifying is trusted from the client on the way through.
			isMigrationIngest := strings.HasPrefix(r.URL.Path, "/services/collector") ||
				(strings.HasPrefix(r.URL.Path, "/v0.") && strings.HasSuffix(r.URL.Path, "/traces")) ||
				// Datadog metrics/logs intake (exact paths, so this can't widen
				// access to PulseTrace's own /api/v1|v2 routes).
				r.URL.Path == "/api/v1/series" || r.URL.Path == "/api/v2/series" || r.URL.Path == "/api/v2/logs"

			if isAgentConfig {
				if tenantID, tier, _, ok := keys.Resolve(r.Context(), bearerToken(r)); ok {
					r.Header.Set("X-Tenant-ID", tenantID)
					r.Header.Set("X-Tenant-Tier", tier)
				} else {
					r.Header.Set("X-Tenant-ID", defaultTenantID)
					r.Header.Set("X-Tenant-Tier", defaultTenantTier)
				}
				next.ServeHTTP(w, r)
				return
			}

			if isServerIngest || isRUMIngest {
				tenantID, tier, scope, ok := keys.Resolve(r.Context(), bearerToken(r))
				switch {
				case ok && isServerIngest && scope != ScopeIngest:
					// A public RUM token (scope 'rum') can attribute browser RUM to a
					// tenant but must never be usable to write server telemetry.
					http.Error(w, "Forbidden: this key is not permitted for server telemetry ingestion", http.StatusForbidden)
					return
				case ok:
					r.Header.Set("X-Tenant-ID", tenantID)
					r.Header.Set("X-Tenant-Tier", tier)
				default:
					if isServerIngest && requireIngestionKey {
						http.Error(w, "Unauthorized: valid ingestion key required", http.StatusUnauthorized)
						return
					}
					r.Header.Set("X-Tenant-ID", defaultTenantID)
					r.Header.Set("X-Tenant-Tier", defaultTenantTier)
				}
				next.ServeHTTP(w, r)
				return
			}

			if isMigrationIngest {
				next.ServeHTTP(w, r)
				return
			}

			// Other public (no-token) endpoints. No tenant is attributed here.
			if r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/register" || r.URL.Path == "/api/v1/auth/signup" || r.URL.Path == "/healthz" ||
				r.URL.Path == "/api/v1/auth/mfa/login" ||
				r.URL.Path == "/api/v1/auth/password/forgot" || r.URL.Path == "/api/v1/auth/password/reset" ||
				r.URL.Path == "/api/v1/auth/sso/login" || r.URL.Path == "/api/v1/auth/sso/config" || r.URL.Path == "/api/v1/auth/sso/callback" ||
				r.URL.Path == "/api/v1/webhooks/stripe" || r.URL.Path == "/api/v1/webhooks/github" || r.URL.Path == "/api/v1/control-plane/incidents" {
				next.ServeHTTP(w, r)
				return
			}

			// Everything else requires a valid JWT.
			tokenStr := bearerToken(r)
			if tokenStr == "" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, errors.New("unexpected signing method")
				}
				return jwtSecret, nil
			})
			if err != nil || !token.Valid {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Set identity headers strictly from signed claims.
			claims, ok := token.Claims.(jwt.MapClaims)
			// A partial MFA-challenge token (issued after password, before the
			// second factor) is validly signed but must NOT authorize protected
			// routes — otherwise MFA could be bypassed by presenting the challenge
			// as a session. It is only accepted by POST /api/v1/auth/mfa/login,
			// which is public and verifies it itself.
			if ok && claims["mfa_pending"] == true {
				http.Error(w, "Unauthorized: complete MFA to obtain a session", http.StatusUnauthorized)
				return
			}
			if ok && token.Valid {
				// Enforce session revocation: a token whose jti was revoked
				// ("log out this device" / "log out everywhere") is rejected even
				// though its signature and expiry are still valid.
				if jti, _ := claims["jti"].(string); jti != "" {
					if sessions.IsRevoked(jti) {
						http.Error(w, "Unauthorized: session has been revoked", http.StatusUnauthorized)
						return
					}
					r.Header.Set("X-Session-ID", jti)
				}
				if role, _ := claims["role"].(string); role != "" {
					r.Header.Set("X-User-Role", role)
				}
				if sub, _ := claims["sub"].(string); sub != "" {
					r.Header.Set("X-User-Subject", sub)
				}
				if tenantID, _ := claims["tenant_id"].(string); tenantID != "" {
					r.Header.Set("X-Tenant-ID", tenantID)
				} else {
					r.Header.Set("X-Tenant-ID", defaultTenantID)
				}
				if tenantTier, _ := claims["tenant_tier"].(string); tenantTier != "" {
					r.Header.Set("X-Tenant-Tier", tenantTier)
				} else {
					r.Header.Set("X-Tenant-Tier", defaultTenantTier)
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	TenantID  string `json:"tenant_id"`
	Tier      string `json:"tier"`
	CreatedAt string `json:"created_at"`
}

// GetUsers handles GET /api/v1/admin/users
func (h *AuthHandler) GetUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rows, err := h.db.Query("SELECT id, username, role, tenant_id, tier, created_at FROM users WHERE tenant_id = $1 ORDER BY created_at DESC", tenantOf(r))
	if err != nil {
		http.Error(w, "Failed to fetch users", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.TenantID, &u.Tier, &u.CreatedAt); err != nil {
			continue
		}
		users = append(users, u)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"data": users})
}

// UpdateUserRole handles PUT /api/v1/admin/users/{id}/role
// In a real router we'd extract {id} from path, here we'll take it from query string for simplicity or parse path.
func (h *AuthHandler) UpdateUserRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.URL.Query().Get("id")
	if userID == "" {
		http.Error(w, "User ID required", http.StatusBadRequest)
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !h.roleExists(req.Role) {
		http.Error(w, "Invalid role", http.StatusBadRequest)
		return
	}

	var previousRole string
	_ = h.db.QueryRow("SELECT role FROM users WHERE id = $1 AND tenant_id = $2", userID, tenantOf(r)).Scan(&previousRole)

	res, err := h.db.Exec("UPDATE users SET role = $1 WHERE id = $2 AND tenant_id = $3", req.Role, userID, tenantOf(r))
	if err != nil {
		http.Error(w, "Failed to update role", http.StatusInternalServerError)
		return
	}
	// A row count of 0 means the id doesn't exist in the caller's tenant — treat
	// it as not found rather than silently succeeding on a cross-tenant id.
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	WriteAudit(h.db, actorFromRequest(r), "update", "user_role", userID,
		map[string]string{"role": previousRole}, map[string]string{"role": req.Role})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// DeleteUser handles DELETE /api/v1/admin/users
func (h *AuthHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.URL.Query().Get("id")
	if userID == "" {
		http.Error(w, "User ID required", http.StatusBadRequest)
		return
	}

	// Prevent deleting the root 'admin' user for safety. Scoped to the caller's
	// tenant so one tenant's admin can't delete another tenant's users by id.
	var username string
	err := h.db.QueryRow("SELECT username FROM users WHERE id = $1 AND tenant_id = $2", userID, tenantOf(r)).Scan(&username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "User not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if username == "admin" {
		http.Error(w, "Cannot delete root admin user", http.StatusForbidden)
		return
	}

	_, err = h.db.Exec("DELETE FROM users WHERE id = $1 AND tenant_id = $2", userID, tenantOf(r))
	if err != nil {
		http.Error(w, "Failed to delete user", http.StatusInternalServerError)
		return
	}

	WriteAudit(h.db, actorFromRequest(r), "delete", "user", userID, map[string]string{"username": username}, nil)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "User deleted successfully"})
}

// CreateUser handles POST /api/v1/admin/users
func (h *AuthHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		TenantID string `json:"tenant_id"`
		Tier     string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Username == "" || req.Password == "" || req.Role == "" {
		http.Error(w, "Username, password, and role are required", http.StatusBadRequest)
		return
	}

	if !h.roleExists(req.Role) {
		http.Error(w, "Invalid role", http.StatusBadRequest)
		return
	}

	// Tenant is the caller's own tenant, not whatever the request body asks for —
	// an admin creates users within their tenant, never into another one.
	tenantID := tenantOf(r)
	tier := req.Tier
	if tier == "" {
		tier = "standard"
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	_, err = h.db.Exec("INSERT INTO users (username, password_hash, role, tenant_id, tier) VALUES ($1, $2, $3, $4, $5)", req.Username, string(hashedPassword), req.Role, tenantID, tier)
	if err != nil {
		if strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "duplicate key") {
			http.Error(w, "Username already exists", http.StatusConflict)
			return
		}
		log.Printf("auth error creating user: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Never write the password (or its hash) to the audit trail.
	WriteAudit(h.db, actorFromRequest(r), "create", "user", req.Username,
		nil, map[string]string{"username": req.Username, "role": req.Role, "tenant_id": tenantID, "tier": tier})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "User created successfully"})
}

// Register handles POST /api/v1/auth/register
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TenantID string `json:"tenant_id"`
		Tier     string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Username == "" || req.Password == "" {
		http.Error(w, "Username and password required", http.StatusBadRequest)
		return
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	tier := req.Tier
	if tier == "" {
		tier = "standard"
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	_, err = h.db.Exec("INSERT INTO users (username, password_hash, role, tenant_id, tier) VALUES ($1, $2, $3, $4, $5)", req.Username, string(hashedPassword), "viewer", tenantID, tier)
	if err != nil {
		if strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "duplicate key") {
			http.Error(w, "Username already exists", http.StatusConflict)
			return
		}
		log.Printf("auth error registering user: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "User registered successfully"})
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// tenantOf returns the caller's tenant from the gateway-verified X-Tenant-ID
// header (set by AuthMiddleware from the JWT). User administration is scoped to
// this tenant so one tenant's admin can neither see nor mutate another tenant's
// users. Defaults to "default".
func tenantOf(r *http.Request) string {
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return defaultTenantID
}

// ── SSO (OAuth2 / OIDC) ───────────────────────────────────────────────────

var googleOauthConfig = &oauth2.Config{
	RedirectURL:  "http://localhost:8080/api/v1/auth/sso/callback",
	ClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
	ClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),
	Scopes:       []string{"https://www.googleapis.com/auth/userinfo.email"},
	Endpoint:     google.Endpoint,
}

const oauthStateCookie = "pt_oauth_state"

// generateOAuthState returns a fresh random CSRF token for a single OAuth
// round-trip. Previously this was a hardcoded constant string, which meant
// the "state" check never actually protected against CSRF — anyone could
// replay the known value. State is now generated per-login and verified
// against a short-lived cookie on callback (see SSOCallback), so it works
// correctly even with multiple gateway replicas and no server-side session store.
func generateOAuthState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// SSOLogin handles GET /api/v1/auth/sso/login
func (h *AuthHandler) SSOLogin(w http.ResponseWriter, r *http.Request) {
	if googleOauthConfig.ClientID == "" {
		http.Error(w, "SSO is not configured on this server.", http.StatusServiceUnavailable)
		return
	}
	state, err := generateOAuthState()
	if err != nil {
		log.Printf("auth: failed to generate OAuth state: %v", err)
		http.Error(w, "Failed to start SSO login", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    state,
		Path:     "/",
		MaxAge:   300, // 5 minutes — plenty for a login redirect round trip
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	url := googleOauthConfig.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// GetSSOConfig handles GET /api/v1/auth/sso/config
func (h *AuthHandler) GetSSOConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"client_id": googleOauthConfig.ClientID,
	})
}

// SSOCallback handles GET /api/v1/auth/sso/callback
func (h *AuthHandler) SSOCallback(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("state")
	cookie, err := r.Cookie(oauthStateCookie)
	if state == "" || err != nil || cookie.Value != state {
		http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
		return
	}
	// Consume the state cookie so it can't be replayed for a second callback.
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: "", Path: "/", MaxAge: -1})

	code := r.FormValue("code")
	if code == "" {
		http.Error(w, "Code not found", http.StatusBadRequest)
		return
	}

	tokenOAuth, err := googleOauthConfig.Exchange(context.Background(), code)
	if err != nil {
		// Mock fallback for local dev when real client ID/Secret isn't provided,
		// but structure is fully production ready.
		log.Printf("OAuth exchange failed (using fallback for local dev): %v", err)
	}

	var email string
	if tokenOAuth != nil && tokenOAuth.Valid() {
		client := googleOauthConfig.Client(context.Background(), tokenOAuth)
		resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
		if err != nil {
			http.Error(w, "Failed to get user info", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		var userInfo struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
			http.Error(w, "Failed to parse user info", http.StatusInternalServerError)
			return
		}
		email = userInfo.Email
	} else {
		// Secure Fallback ONLY when explicit mock client ID is used (Dev Mode)
		if googleOauthConfig.ClientID == "mock-client-id" {
			email = "demo.sso@company.com"
		} else {
			http.Error(w, "Failed to exchange token", http.StatusInternalServerError)
			return
		}
	}

	if email == "" {
		http.Error(w, "Email not found in OAuth token", http.StatusBadRequest)
		return
	}

	// 1. Check if user exists, if not auto-provision
	var storedHash, role, tenantID, tier string
	err = h.db.QueryRow("SELECT password_hash, role, tenant_id, tier FROM users WHERE username = $1", email).Scan(&storedHash, &role, &tenantID, &tier)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Auto-provision the SSO user as a 'viewer'
			tenantID = "default"
			tier = "standard"
			role = "viewer"
			// Create a random dummy password hash since they authenticate via SSO —
			// this account should never be logged into via the password flow.
			dummySecret, err := generateOAuthState() // reuse: 32 random bytes, base64-encoded
			if err != nil {
				http.Error(w, "Failed to auto-provision SSO user", http.StatusInternalServerError)
				return
			}
			dummyHash, _ := bcrypt.GenerateFromPassword([]byte(dummySecret+email), bcrypt.DefaultCost)
			_, err = h.db.Exec("INSERT INTO users (username, password_hash, role, tenant_id, tier) VALUES ($1, $2, $3, $4, $5)", email, string(dummyHash), role, tenantID, tier)
			if err != nil {
				http.Error(w, "Failed to auto-provision SSO user", http.StatusInternalServerError)
				return
			}
		} else {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
	}

	// 2. Generate PulseTrace JWT
	jti := createSession(r.Context(), h.db, email, tenantID, r.UserAgent(), clientIP(r))
	tokenString, err := issueSessionToken(email, role, tenantID, tier, jti)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	// 3. Redirect back to frontend with the token
	// In a real app, use a secure cookie instead of URL fragments
	redirectURL := "http://localhost:3000/settings?token=" + tokenString
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}
