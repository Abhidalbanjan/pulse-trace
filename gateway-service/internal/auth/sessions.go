package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Session management (F18): make stateless JWTs revocable.
//
// Each issued dashboard session is recorded in user_sessions keyed by the
// token's jti. AuthMiddleware consults an in-memory cache of revoked jtis
// (refreshed periodically, and updated instantly on the revoking node) so the
// per-request cost is a map lookup, not a database round-trip. Revoking a
// session — one device, or "everywhere else" — flips revoked_at and the next
// request bearing that token is rejected.

const (
	// sessionCacheTTL bounds how long a revocation made on another gateway
	// replica can take to take effect here (the revoking replica applies it
	// instantly to its own cache). 30s matches the RBAC engine's staleness bar.
	sessionCacheTTL = 30 * time.Second
	// revokedRetention is how far back the cache loads revoked sessions. Beyond
	// the token TTL a revoked token is already expired, so there is no need to
	// keep matching against it.
	revokedRetention = sessionTokenTTL + time.Hour
)

// createSession records a new session row and returns its id (the jti to embed
// in the token). Best-effort: if the row can't be written the login still
// succeeds with a valid jti — the session just won't be listed or revocable —
// because failing a correct login over a bookkeeping error is the worse outcome.
func createSession(ctx context.Context, db *sql.DB, username, tenantID, userAgent, ip string) string {
	jti := uuid.NewString()
	if db == nil {
		return jti
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_sessions (id, username, tenant_id, user_agent, ip) VALUES ($1, $2, $3, $4, $5)`,
		jti, username, tenantID, truncate(userAgent, 512), truncate(ip, 64)); err != nil {
		log.Printf("sessions: failed to record session for %s: %v", username, err)
	}
	return jti
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// SessionStore holds the revoked-jti cache consulted by AuthMiddleware.
type SessionStore struct {
	db *sql.DB

	mu          sync.RWMutex
	revoked     map[string]struct{}
	lastRefresh time.Time
}

func NewSessionStore(db *sql.DB) *SessionStore {
	s := &SessionStore{db: db, revoked: map[string]struct{}{}}
	s.refresh()
	return s
}

func (s *SessionStore) refresh() {
	if s.db == nil {
		return
	}
	rows, err := s.db.Query(
		`SELECT id FROM user_sessions WHERE revoked_at IS NOT NULL AND revoked_at > now() - $1::interval`,
		revokedRetention.String())
	if err != nil {
		log.Printf("sessions: revoked-cache refresh failed: %v", err)
		return
	}
	defer rows.Close()
	next := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			next[id] = struct{}{}
		}
	}
	s.mu.Lock()
	s.revoked = next
	s.lastRefresh = time.Now()
	s.mu.Unlock()
}

func (s *SessionStore) refreshIfStale() {
	s.mu.RLock()
	stale := time.Since(s.lastRefresh) > sessionCacheTTL
	s.mu.RUnlock()
	if stale {
		s.refresh()
	}
}

// IsRevoked reports whether a token's jti has been revoked. An empty jti (a
// legacy token issued before session tracking) is treated as not revoked.
func (s *SessionStore) IsRevoked(jti string) bool {
	if s == nil || jti == "" {
		return false
	}
	s.refreshIfStale()
	s.mu.RLock()
	_, ok := s.revoked[jti]
	s.mu.RUnlock()
	return ok
}

func (s *SessionStore) markRevokedLocal(jtis ...string) {
	s.mu.Lock()
	for _, j := range jtis {
		if j != "" {
			s.revoked[j] = struct{}{}
		}
	}
	s.mu.Unlock()
}

// ── HTTP handlers ──────────────────────────────────────────────────────────

// SessionHandler serves the caller's own session list and revocation actions.
type SessionHandler struct {
	store *SessionStore
	db    *sql.DB
}

func NewSessionHandler(store *SessionStore, db *sql.DB) *SessionHandler {
	return &SessionHandler{store: store, db: db}
}

// sessionInfo is one row in the device list.
type sessionInfo struct {
	ID         string `json:"id"`
	UserAgent  string `json:"user_agent"`
	IP         string `json:"ip"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	Current    bool   `json:"current"`
}

// currentSession returns the jti of the request's own token, set by
// AuthMiddleware from the verified claims.
func currentSession(r *http.Request) string { return r.Header.Get("X-Session-ID") }

// List handles GET /api/v1/auth/sessions — the caller's active sessions, newest
// first, with their own session flagged.
func (h *SessionHandler) List(w http.ResponseWriter, r *http.Request) {
	user := actorFromRequest(r)
	if user == "" {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, COALESCE(user_agent, ''), COALESCE(ip, ''), created_at::text, last_seen_at::text
		FROM user_sessions
		WHERE username = $1 AND revoked_at IS NULL
		ORDER BY created_at DESC
		LIMIT 100`, user)
	if err != nil {
		http.Error(w, "failed to list sessions", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	current := currentSession(r)
	out := []sessionInfo{}
	for rows.Next() {
		var s sessionInfo
		if err := rows.Scan(&s.ID, &s.UserAgent, &s.IP, &s.CreatedAt, &s.LastSeenAt); err != nil {
			continue
		}
		s.Current = s.ID == current
		out = append(out, s)
	}
	writeSessionJSON(w, map[string]interface{}{"data": out})
}

// Revoke handles POST /api/v1/auth/sessions/{id}/revoke — revoke a single
// session the caller owns.
func (h *SessionHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	user := actorFromRequest(r)
	if user == "" {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	res, err := h.db.ExecContext(r.Context(),
		`UPDATE user_sessions SET revoked_at = now() WHERE id = $1 AND username = $2 AND revoked_at IS NULL`, id, user)
	if err != nil {
		http.Error(w, "failed to revoke session", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	h.store.markRevokedLocal(id)
	writeSessionJSON(w, map[string]interface{}{"revoked": id})
}

// RevokeOthers handles POST /api/v1/auth/sessions/revoke-others — sign out every
// device except the one making the request (the classic "secure my account").
func (h *SessionHandler) RevokeOthers(w http.ResponseWriter, r *http.Request) {
	user := actorFromRequest(r)
	if user == "" {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	current := currentSession(r)
	rows, err := h.db.QueryContext(r.Context(),
		`UPDATE user_sessions SET revoked_at = now()
		 WHERE username = $1 AND id <> $2 AND revoked_at IS NULL
		 RETURNING id`, user, current)
	if err != nil {
		http.Error(w, "failed to revoke sessions", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var revoked []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			revoked = append(revoked, id)
		}
	}
	h.store.markRevokedLocal(revoked...)
	writeSessionJSON(w, map[string]interface{}{"revoked_count": len(revoked)})
}

func writeSessionJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
