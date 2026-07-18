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
	err := h.db.QueryRow("SELECT password_hash, role, tenant_id, tier FROM users WHERE username = $1", creds.Username).Scan(&storedHash, &role, &tenantID, &tier)
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

	// Generate JWT
	expirationTime := time.Now().Add(24 * time.Hour)
	claims := &jwt.RegisteredClaims{
		Subject:   creds.Username,
		ExpiresAt: jwt.NewNumericDate(expirationTime),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         claims.Subject,
		"exp":         claims.ExpiresAt,
		"iat":         claims.IssuedAt,
		"role":        role,
		"tenant_id":   tenantID,
		"tenant_tier": tier,
	})

	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TokenResponse{Token: tokenString, Role: role})
}

// AuthMiddleware validates the JWT token
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow login, registration, OTLP, control plane, log ingestion (POST), and RUM
		// ingestion (POST) endpoints without token. RUM specifically must stay
		// unauthenticated: it captures page views/errors/web-vitals for every visitor,
		// including anonymous sessions on public pages like /login before sign-in.
		isLogIngest := r.URL.Path == "/api/v1/logs" && r.Method == http.MethodPost
		isRUMIngest := r.URL.Path == "/api/v1/rum/ingest" && r.Method == http.MethodPost
		if r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/register" || r.URL.Path == "/healthz" ||
			r.URL.Path == "/api/v1/auth/sso/login" || r.URL.Path == "/api/v1/auth/sso/callback" ||
			strings.HasPrefix(r.URL.Path, "/v1/traces") || strings.HasPrefix(r.URL.Path, "/v1/metrics") || strings.HasPrefix(r.URL.Path, "/v1/logs") ||
			strings.HasPrefix(r.URL.Path, "/api/v1/topology/agent-config") || r.URL.Path == "/api/v1/control-plane/incidents" || isLogIngest || isRUMIngest {

			// For unauthenticated telemetry endpoints, propagate tenant headers if provided in request
			if isLogIngest || strings.HasPrefix(r.URL.Path, "/v1/traces") || strings.HasPrefix(r.URL.Path, "/v1/metrics") || strings.HasPrefix(r.URL.Path, "/v1/logs") {
				tenantID := r.Header.Get("X-Tenant-ID")
				if tenantID == "" {
					tenantID = "default"
				}
				tenantTier := r.Header.Get("X-Tenant-Tier")
				if tenantTier == "" {
					tenantTier = "standard"
				}
				r.Header.Set("X-Tenant-ID", tenantID)
				r.Header.Set("X-Tenant-Tier", tenantTier)
			}

			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
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

		// Extract claims and pass to context if needed
		claims, ok := token.Claims.(jwt.MapClaims)
		if ok && token.Valid {
			r.Header.Set("X-User-Role", claims["role"].(string))
			r.Header.Set("X-User-Subject", claims["sub"].(string))
			if tenantID, exists := claims["tenant_id"].(string); exists {
				r.Header.Set("X-Tenant-ID", tenantID)
			} else {
				r.Header.Set("X-Tenant-ID", "default")
			}
			if tenantTier, exists := claims["tenant_tier"].(string); exists {
				r.Header.Set("X-Tenant-Tier", tenantTier)
			} else {
				r.Header.Set("X-Tenant-Tier", "standard")
			}
		}

		next.ServeHTTP(w, r)
	})
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

	rows, err := h.db.Query("SELECT id, username, role, tenant_id, tier, created_at FROM users ORDER BY created_at DESC")
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
	_ = h.db.QueryRow("SELECT role FROM users WHERE id = $1", userID).Scan(&previousRole)

	_, err := h.db.Exec("UPDATE users SET role = $1 WHERE id = $2", req.Role, userID)
	if err != nil {
		http.Error(w, "Failed to update role", http.StatusInternalServerError)
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

	// Prevent deleting the root 'admin' user for safety
	var username string
	err := h.db.QueryRow("SELECT username FROM users WHERE id = $1", userID).Scan(&username)
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

	_, err = h.db.Exec("DELETE FROM users WHERE id = $1", userID)
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
	expirationTime := time.Now().Add(24 * time.Hour)
	claims := &jwt.RegisteredClaims{
		Subject:   email,
		ExpiresAt: jwt.NewNumericDate(expirationTime),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         claims.Subject,
		"exp":         claims.ExpiresAt,
		"iat":         claims.IssuedAt,
		"role":        role,
		"tenant_id":   tenantID,
		"tenant_tier": tier,
	})

	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	// 3. Redirect back to frontend with the token
	// In a real app, use a secure cookie instead of URL fragments
	redirectURL := "http://localhost:3000/settings?token=" + tokenString
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}
