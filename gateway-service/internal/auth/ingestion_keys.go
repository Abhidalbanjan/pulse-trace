package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Key scopes. ScopeIngest is a full, secret server-side key; ScopeRUM is a
// public, RUM-only client token safe to embed in browser JS (see migration 011).
const (
	ScopeIngest = "ingest"
	ScopeRUM    = "rum"
)

// Plaintext prefixes are human-recognizable so a leaked key is greppable in logs
// and secret scanners, and the prefix alone tells you which scope it is.
const (
	ingestKeyPrefix = "pt_ingest_"
	rumKeyPrefix    = "pt_rum_"
)

func prefixForScope(scope string) string {
	if scope == ScopeRUM {
		return rumKeyPrefix
	}
	return ingestKeyPrefix
}

// ingestionKeyCacheTTL bounds how long a resolved (or rejected) key is trusted
// from the in-process cache before we re-check Postgres. Ingestion is high-QPS
// (the telemetry-ingest rate limit alone allows thousands/min), so a DB round
// trip per request would be wasteful; a short TTL keeps revocations near-instant
// while collapsing the steady-state load to one lookup per key per TTL.
const ingestionKeyCacheTTL = 30 * time.Second

// resolvedTenant is what an ingestion key maps to once verified.
type resolvedTenant struct {
	tenantID string
	tier     string
	scope    string
	ok       bool // false = a real negative result (unknown/revoked key), cached to blunt lookup floods
	cachedAt time.Time
}

// IngestionKeyStore verifies per-tenant ingestion keys against Postgres with a
// short-lived in-memory cache. It is the single source of truth for "which
// tenant does this ingestion request belong to" — that answer is never taken
// from a client-supplied header. Safe for concurrent use.
type IngestionKeyStore struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache map[string]resolvedTenant // keyed by SHA-256 hex of the plaintext
}

func NewIngestionKeyStore(db *sql.DB) *IngestionKeyStore {
	return &IngestionKeyStore{db: db, cache: make(map[string]resolvedTenant)}
}

// hashIngestionKey returns the SHA-256 hex digest we store and look up by. The
// key has enough entropy (256 bits) that a plain fast hash is appropriate here
// — unlike a human password, it isn't guessable, so we want cheap verification
// on the hot path rather than a deliberately slow KDF.
func hashIngestionKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// generateIngestionKey mints a fresh key of the given scope, returning the
// plaintext (shown to the operator once), a non-secret display prefix, and the
// hash to persist.
func generateIngestionKey(scope string) (plaintext, prefix, hash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	humanPrefix := prefixForScope(scope)
	plaintext = humanPrefix + base64.RawURLEncoding.EncodeToString(raw)
	// First few chars past the human prefix, enough to disambiguate in a list
	// without leaking anything sensitive.
	prefix = plaintext[:len(humanPrefix)+6]
	hash = hashIngestionKey(plaintext)
	return plaintext, prefix, hash, nil
}

// Resolve verifies a presented plaintext key and returns the tenant/tier/scope it
// belongs to. ok is false for an empty, unknown, or revoked key. Results
// (including negatives) are cached for ingestionKeyCacheTTL.
func (s *IngestionKeyStore) Resolve(ctx context.Context, plaintext string) (tenantID, tier, scope string, ok bool) {
	if plaintext == "" || s == nil || s.db == nil {
		return "", "", "", false
	}
	hash := hashIngestionKey(plaintext)

	s.mu.RLock()
	entry, cached := s.cache[hash]
	s.mu.RUnlock()
	if cached && time.Since(entry.cachedAt) < ingestionKeyCacheTTL {
		return entry.tenantID, entry.tier, entry.scope, entry.ok
	}

	var resolved resolvedTenant
	resolved.cachedAt = time.Now()
	// A key is valid while it has no revocation, OR its revocation is still in the
	// future — the grace window a rotation opens so a public RUM token embedded in
	// already-served pages keeps working until clients pick up the replacement.
	// The 30s resolve cache means the cutover takes effect within a TTL of the
	// grace expiry, which is fine for a scheduled rotation.
	err := s.db.QueryRowContext(ctx,
		"SELECT tenant_id, tier, scope FROM ingestion_keys WHERE key_hash = $1 AND (revoked_at IS NULL OR revoked_at > now())",
		hash,
	).Scan(&resolved.tenantID, &resolved.tier, &resolved.scope)
	switch {
	case err == nil:
		resolved.ok = true
		// Best-effort, throttled to once per cache miss (i.e. ~once per TTL per
		// key): record activity without adding a write to the request hot path.
		go s.touchLastUsed(hash)
	case errors.Is(err, sql.ErrNoRows):
		resolved.ok = false
	default:
		// On a transient DB error, don't cache and don't guess — reject this
		// request so a Postgres blip can't silently mis-attribute tenant data.
		log.Printf("ingestion_keys: lookup failed: %v", err)
		return "", "", "", false
	}

	s.mu.Lock()
	s.cache[hash] = resolved
	s.mu.Unlock()
	return resolved.tenantID, resolved.tier, resolved.scope, resolved.ok
}

func (s *IngestionKeyStore) touchLastUsed(hash string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, "UPDATE ingestion_keys SET last_used_at = now() WHERE key_hash = $1", hash); err != nil {
		log.Printf("ingestion_keys: failed to update last_used_at: %v", err)
	}
}

// invalidateCache drops the whole cache so a just-created or just-revoked key
// takes effect immediately rather than after the TTL. The cache is small
// (one entry per active key), so clearing it wholesale is cheap and simpler
// than surgical eviction.
func (s *IngestionKeyStore) invalidateCache() {
	s.mu.Lock()
	s.cache = make(map[string]resolvedTenant)
	s.mu.Unlock()
}

// ── Admin API (mounted under /api/v1/admin, so RBACEngine.Middleware gates it
// exactly like roles/policies/rate-limits) ────────────────────────────────────

type ingestionKeyView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	KeyPrefix  string  `json:"key_prefix"`
	TenantID   string  `json:"tenant_id"`
	Tier       string  `json:"tier"`
	Scope      string  `json:"scope"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	// RevokedAt may be in the future (a rotation's grace window): the key stays
	// valid until then. ReplacedBy links a rotated-out key to its successor.
	RevokedAt  *string `json:"revoked_at"`
	ReplacedBy *string `json:"replaced_by"`
}

// ListIngestionKeys handles GET /api/v1/admin/ingestion-keys. It never returns
// the plaintext or the hash — only the non-secret metadata needed to manage keys.
func (s *IngestionKeyStore) ListIngestionKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.db == nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []ingestionKeyView{}})
		return
	}
	rows, err := s.db.Query(`
		SELECT id, name, key_prefix, tenant_id, tier, scope,
		       created_at::text, last_used_at::text, revoked_at::text, replaced_by::text
		FROM ingestion_keys
		ORDER BY created_at DESC`)
	if err != nil {
		log.Printf("ingestion_keys: failed to list: %v", err)
		http.Error(w, "failed to list ingestion keys", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []ingestionKeyView{}
	for rows.Next() {
		var v ingestionKeyView
		if err := rows.Scan(&v.ID, &v.Name, &v.KeyPrefix, &v.TenantID, &v.Tier, &v.Scope, &v.CreatedAt, &v.LastUsedAt, &v.RevokedAt, &v.ReplacedBy); err != nil {
			continue
		}
		out = append(out, v)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}

// CreateIngestionKey handles POST /api/v1/admin/ingestion-keys. The generated
// plaintext key is returned in the response body ONCE and never again — the
// client must capture it here.
func (s *IngestionKeyStore) CreateIngestionKey(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "ingestion keys unavailable (no database)", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Name     string `json:"name"`
		TenantID string `json:"tenant_id"`
		Tier     string `json:"tier"`
		Scope    string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}
	if req.Tier == "" {
		req.Tier = "standard"
	}
	if req.Scope == "" {
		req.Scope = ScopeIngest
	}
	if req.Scope != ScopeIngest && req.Scope != ScopeRUM {
		http.Error(w, "scope must be 'ingest' or 'rum'", http.StatusBadRequest)
		return
	}

	plaintext, prefix, hash, err := generateIngestionKey(req.Scope)
	if err != nil {
		http.Error(w, "failed to generate key", http.StatusInternalServerError)
		return
	}

	var id string
	err = s.db.QueryRow(
		"INSERT INTO ingestion_keys (name, key_prefix, key_hash, tenant_id, tier, scope) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id",
		req.Name, prefix, hash, req.TenantID, req.Tier, req.Scope,
	).Scan(&id)
	if err != nil {
		log.Printf("ingestion_keys: failed to create: %v", err)
		http.Error(w, "failed to create ingestion key", http.StatusInternalServerError)
		return
	}
	s.invalidateCache()

	// Audit the creation, but never write the plaintext or hash to the trail.
	WriteAudit(s.db, actorFromRequest(r), "create", "ingestion_key", id,
		nil, map[string]string{"name": req.Name, "tenant_id": req.TenantID, "tier": req.Tier, "scope": req.Scope, "key_prefix": prefix})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":         id,
		"name":       req.Name,
		"tenant_id":  req.TenantID,
		"tier":       req.Tier,
		"scope":      req.Scope,
		"key_prefix": prefix,
		// Shown exactly once. There is no endpoint that can return it again.
		"key":     plaintext,
		"warning": "Store this key now — it cannot be retrieved again.",
	})
}

// RevokeIngestionKey handles DELETE /api/v1/admin/ingestion-keys/{id}. Revocation
// is a soft-delete (sets revoked_at); the row stays for audit history.
func (s *IngestionKeyStore) RevokeIngestionKey(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "ingestion keys unavailable (no database)", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "key id required", http.StatusBadRequest)
		return
	}
	// Revoke immediately, including a key whose revocation was only scheduled for
	// the future (a rotation grace window) — this lets an operator kill a
	// compromised key now, cancelling any grace. Only an already-past revocation
	// is a no-op.
	res, err := s.db.Exec("UPDATE ingestion_keys SET revoked_at = now() WHERE id = $1 AND (revoked_at IS NULL OR revoked_at > now())", id)
	if err != nil {
		log.Printf("ingestion_keys: failed to revoke: %v", err)
		http.Error(w, "failed to revoke ingestion key", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "key not found or already revoked", http.StatusNotFound)
		return
	}
	s.invalidateCache()
	WriteAudit(s.db, actorFromRequest(r), "revoke", "ingestion_key", id, nil, nil)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
}

// defaultRotationGrace is how long a rotated-out key keeps working by default, so
// a public RUM token in already-served browser pages doesn't break the instant it
// is rotated. Overridable per request; "0" revokes the old key immediately.
const defaultRotationGrace = 24 * time.Hour

// maxRotationGrace caps the grace window so a rotation can't leave an old key
// live indefinitely.
const maxRotationGrace = 30 * 24 * time.Hour

// RotateIngestionKey handles POST /api/v1/admin/ingestion-keys/{id}/rotate. It
// mints a replacement key inheriting the old one's tenant/tier/scope/name, links
// the old key to it (replaced_by), and schedules the old key's revocation after a
// grace window (default 24h; body {"grace_period":"0"} revokes immediately). The
// new plaintext is returned ONCE, exactly like create.
//
// Rotation is the missing lifecycle piece for public RUM tokens specifically:
// they're embedded in browser pages you can't instantly re-serve, so a hard
// revoke-and-replace would drop RUM from every cached page until it reloaded. The
// grace window lets the new token propagate first.
func (s *IngestionKeyStore) RotateIngestionKey(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "ingestion keys unavailable (no database)", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "key id required", http.StatusBadRequest)
		return
	}

	var req struct {
		GracePeriod string `json:"grace_period"` // Go duration, e.g. "24h", "0"
	}
	// Body is optional; a decode error just means "use the default grace".
	_ = json.NewDecoder(r.Body).Decode(&req)

	grace := defaultRotationGrace
	if strings.TrimSpace(req.GracePeriod) != "" {
		d, err := time.ParseDuration(req.GracePeriod)
		if err != nil || d < 0 {
			http.Error(w, "invalid grace_period (want a non-negative Go duration like \"24h\" or \"0\")", http.StatusBadRequest)
			return
		}
		if d > maxRotationGrace {
			http.Error(w, "grace_period exceeds the 30d maximum", http.StatusBadRequest)
			return
		}
		grace = d
	}

	// Load the key being rotated; only a currently-valid key can be rotated (a
	// fully-revoked one has no live successor to grace into).
	var name, tenantID, tier, scope string
	err := s.db.QueryRow(
		"SELECT name, tenant_id, tier, scope FROM ingestion_keys WHERE id = $1 AND (revoked_at IS NULL OR revoked_at > now())",
		id,
	).Scan(&name, &tenantID, &tier, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "key not found or already revoked", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("ingestion_keys: failed to load key for rotation: %v", err)
		http.Error(w, "failed to rotate ingestion key", http.StatusInternalServerError)
		return
	}

	plaintext, prefix, hash, err := generateIngestionKey(scope)
	if err != nil {
		http.Error(w, "failed to generate key", http.StatusInternalServerError)
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, "failed to rotate ingestion key", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var newID string
	if err := tx.QueryRow(
		"INSERT INTO ingestion_keys (name, key_prefix, key_hash, tenant_id, tier, scope) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id",
		name, prefix, hash, tenantID, tier, scope,
	).Scan(&newID); err != nil {
		log.Printf("ingestion_keys: failed to insert rotated key: %v", err)
		http.Error(w, "failed to rotate ingestion key", http.StatusInternalServerError)
		return
	}

	// Point the old key at its successor and schedule its revocation. grace=0 →
	// revoked immediately (now()); otherwise revoked at now()+grace.
	if _, err := tx.Exec(
		"UPDATE ingestion_keys SET replaced_by = $1, revoked_at = now() + make_interval(secs => $2) WHERE id = $3",
		newID, grace.Seconds(), id,
	); err != nil {
		log.Printf("ingestion_keys: failed to schedule old key revocation: %v", err)
		http.Error(w, "failed to rotate ingestion key", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("ingestion_keys: failed to commit rotation: %v", err)
		http.Error(w, "failed to rotate ingestion key", http.StatusInternalServerError)
		return
	}
	s.invalidateCache()

	WriteAudit(s.db, actorFromRequest(r), "rotate", "ingestion_key", id,
		map[string]string{"replaced_by": newID}, map[string]string{"grace_period": grace.String(), "new_key_prefix": prefix})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":            newID,
		"rotated_from":  id,
		"name":          name,
		"tenant_id":     tenantID,
		"tier":          tier,
		"scope":         scope,
		"key_prefix":    prefix,
		"grace_period":  grace.String(),
		"old_key_valid_until": time.Now().Add(grace).UTC().Format(time.RFC3339),
		// Shown exactly once, like create.
		"key":     plaintext,
		"warning": "Store this key now — it cannot be retrieved again. The previous key keeps working until the grace window expires.",
	})
}
