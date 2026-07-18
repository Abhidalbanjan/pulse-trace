package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pulsetrace/gateway-service/internal/auth"
	"github.com/pulsetrace/shared/middleware"
)

// RateLimitRuleHandler manages rate_limit_rules in Postgres and keeps a live
// *middleware.RateLimiter in sync via periodic polling - the same "DB-backed,
// no-restart" pattern used for RBAC roles/policies.
type RateLimitRuleHandler struct {
	db      *sql.DB
	limiter *middleware.RateLimiter
}

func NewRateLimitRuleHandler(db *sql.DB, limiter *middleware.RateLimiter) *RateLimitRuleHandler {
	h := &RateLimitRuleHandler{db: db, limiter: limiter}
	h.refresh()
	return h
}

type rateLimitRuleRow struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	PathPrefixes  []string `json:"path_prefixes"`
	LimitCount    int      `json:"limit_count"`
	WindowSeconds int      `json:"window_seconds"`
	Priority      int      `json:"priority"`
	Enabled       bool     `json:"enabled"`
}

// StartPolling refreshes the live limiter from Postgres every interval, so an
// admin's create/update/delete call (which also calls refresh() immediately)
// stays correct even across gateway-service replicas that didn't serve the
// mutating request themselves.
func (h *RateLimitRuleHandler) StartPolling(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.refresh()
			}
		}
	}()
}

func (h *RateLimitRuleHandler) loadRules() ([]rateLimitRuleRow, error) {
	rows, err := h.db.Query("SELECT id, name, path_prefixes, limit_count, window_seconds, priority, enabled FROM rate_limit_rules WHERE enabled = true ORDER BY priority ASC, name ASC, id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []rateLimitRuleRow
	for rows.Next() {
		var r rateLimitRuleRow
		var prefixesJSON []byte
		if err := rows.Scan(&r.ID, &r.Name, &prefixesJSON, &r.LimitCount, &r.WindowSeconds, &r.Priority, &r.Enabled); err != nil {
			continue
		}
		_ = json.Unmarshal(prefixesJSON, &r.PathPrefixes)
		out = append(out, r)
	}
	return out, nil
}

func (h *RateLimitRuleHandler) refresh() {
	if h.db == nil || h.limiter == nil {
		return
	}
	rows, err := h.loadRules()
	if err != nil {
		log.Printf("ratelimit: failed to load rules from postgres, keeping previous rules: %v", err)
		return
	}
	rules := make([]middleware.RateLimitRule, 0, len(rows))
	for _, r := range rows {
		rules = append(rules, middleware.RateLimitRule{
			Name:         r.Name,
			PathPrefixes: r.PathPrefixes,
			Limit:        r.LimitCount,
			Window:       time.Duration(r.WindowSeconds) * time.Second,
		})
	}
	h.limiter.SetRules(rules)
}

// ListRateLimitRules handles GET /api/v1/admin/rate-limits
func (h *RateLimitRuleHandler) ListRateLimitRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, err := h.db.Query("SELECT id, name, path_prefixes, limit_count, window_seconds, priority, enabled FROM rate_limit_rules ORDER BY priority ASC, name ASC")
	if err != nil {
		log.Printf("ratelimit: failed to list rules: %v", err)
		http.Error(w, "failed to list rate limit rules", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []rateLimitRuleRow{}
	for rows.Next() {
		var rr rateLimitRuleRow
		var prefixesJSON []byte
		if err := rows.Scan(&rr.ID, &rr.Name, &prefixesJSON, &rr.LimitCount, &rr.WindowSeconds, &rr.Priority, &rr.Enabled); err != nil {
			continue
		}
		_ = json.Unmarshal(prefixesJSON, &rr.PathPrefixes)
		out = append(out, rr)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}

type upsertRateLimitRuleRequest struct {
	Name          string   `json:"name"`
	PathPrefixes  []string `json:"path_prefixes"`
	LimitCount    int      `json:"limit_count"`
	WindowSeconds int      `json:"window_seconds"`
	Priority      int      `json:"priority"`
	Enabled       *bool    `json:"enabled"`
}

// CreateRateLimitRule handles POST /api/v1/admin/rate-limits
func (h *RateLimitRuleHandler) CreateRateLimitRule(w http.ResponseWriter, r *http.Request) {
	var req upsertRateLimitRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || len(req.PathPrefixes) == 0 || req.LimitCount <= 0 || req.WindowSeconds <= 0 {
		http.Error(w, "name, path_prefixes, limit_count (>0), and window_seconds (>0) are required", http.StatusBadRequest)
		return
	}
	prefixesJSON, _ := json.Marshal(req.PathPrefixes)
	_, err := h.db.Exec(
		"INSERT INTO rate_limit_rules (name, path_prefixes, limit_count, window_seconds, priority) VALUES ($1, $2, $3, $4, $5)",
		req.Name, prefixesJSON, req.LimitCount, req.WindowSeconds, req.Priority,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			http.Error(w, "a rule with that name already exists", http.StatusConflict)
			return
		}
		log.Printf("ratelimit: failed to create rule: %v", err)
		http.Error(w, "failed to create rate limit rule", http.StatusInternalServerError)
		return
	}
	h.refresh()
	auth.WriteAudit(h.db, actorFromHeader(r), "create", "rate_limit_rule", req.Name, nil, req)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

// UpdateRateLimitRule handles PUT /api/v1/admin/rate-limits/{id}
func (h *RateLimitRuleHandler) UpdateRateLimitRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req upsertRateLimitRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	prefixesJSON, _ := json.Marshal(req.PathPrefixes)
	res, err := h.db.Exec(
		`UPDATE rate_limit_rules SET
			path_prefixes = CASE WHEN $1::jsonb = '[]'::jsonb THEN path_prefixes ELSE $1::jsonb END,
			limit_count = COALESCE(NULLIF($2, 0), limit_count),
			window_seconds = COALESCE(NULLIF($3, 0), window_seconds),
			priority = $4,
			enabled = $5,
			updated_at = now()
		WHERE id = $6`,
		prefixesJSON, req.LimitCount, req.WindowSeconds, req.Priority, enabled, id,
	)
	if err != nil {
		log.Printf("ratelimit: failed to update rule: %v", err)
		http.Error(w, "failed to update rate limit rule", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "rate limit rule not found", http.StatusNotFound)
		return
	}
	h.refresh()
	auth.WriteAudit(h.db, actorFromHeader(r), "update", "rate_limit_rule", id, nil, req)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// DeleteRateLimitRule handles DELETE /api/v1/admin/rate-limits/{id}
func (h *RateLimitRuleHandler) DeleteRateLimitRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := h.db.Exec("DELETE FROM rate_limit_rules WHERE id = $1", id)
	if err != nil {
		log.Printf("ratelimit: failed to delete rule: %v", err)
		http.Error(w, "failed to delete rate limit rule", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "rate limit rule not found", http.StatusNotFound)
		return
	}
	h.refresh()
	auth.WriteAudit(h.db, actorFromHeader(r), "delete", "rate_limit_rule", id, nil, nil)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

func actorFromHeader(r *http.Request) string {
	return r.Header.Get("X-User-Subject")
}
