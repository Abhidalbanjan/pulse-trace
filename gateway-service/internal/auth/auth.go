package auth

import (
	"database/sql"
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
)

var jwtSecret = []byte(getEnv("JWT_SECRET", "pulsetrace-super-secret-key-change-in-prod"))

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
		// Allow login, registration, OTLP, control plane, and log ingestion (POST) endpoints without token
		isLogIngest := r.URL.Path == "/api/v1/logs" && r.Method == http.MethodPost
		if r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/register" || r.URL.Path == "/healthz" ||
			strings.HasPrefix(r.URL.Path, "/v1/traces") || strings.HasPrefix(r.URL.Path, "/v1/metrics") || strings.HasPrefix(r.URL.Path, "/v1/logs") ||
			strings.HasPrefix(r.URL.Path, "/api/v1/topology/agent-config") || r.URL.Path == "/api/v1/control-plane/incidents" || isLogIngest {
			
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

// RBACMiddleware ensures only admins can access sensitive endpoints
func RBACMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-User-Role")
		
		// Endpoints that require 'admin' role
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin") || strings.HasPrefix(r.URL.Path, "/api/v1/settings") {
			if role != "admin" {
				http.Error(w, "Forbidden: Admin access required", http.StatusForbidden)
				return
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

	if req.Role != "admin" && req.Role != "viewer" {
		http.Error(w, "Invalid role", http.StatusBadRequest)
		return
	}

	_, err := h.db.Exec("UPDATE users SET role = $1 WHERE id = $2", req.Role, userID)
	if err != nil {
		http.Error(w, "Failed to update role", http.StatusInternalServerError)
		return
	}

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

	if req.Role != "admin" && req.Role != "viewer" {
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
