package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimitRule defines a fixed-window request budget for one class of route.
type RateLimitRule struct {
	Name string // identifies the rule for admin UI / audit purposes
	// PathPrefixes selects which requests this rule applies to. The first
	// matching rule (in the order passed to NewRateLimiter/SetRules) wins.
	PathPrefixes []string
	Limit        int // max requests per Window
	Window       time.Duration
}

// RateLimiter is a Redis-backed fixed-window limiter shared across every
// gateway-service replica (hence "distributed" - the counters live in Redis,
// not in each instance's memory, so the limit holds regardless of how many
// gateway pods are running behind the load balancer).
//
// Rules are mutable at runtime via SetRules (gateway-service polls a Postgres
// rate_limit_rules table and pushes updates here every few seconds), so an
// admin can add/edit/remove a rule from the UI without a redeploy.
type RateLimiter struct {
	rdb   *redis.Client
	mu    sync.RWMutex
	rules []RateLimitRule
}

func NewRateLimiter(redisAddr string, rules []RateLimitRule) *RateLimiter {
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	return &RateLimiter{rdb: rdb, rules: rules}
}

// SetRules atomically replaces the active rule set.
func (rl *RateLimiter) SetRules(rules []RateLimitRule) {
	rl.mu.Lock()
	rl.rules = rules
	rl.mu.Unlock()
}

// Rules returns a snapshot of the active rule set (for admin/status APIs).
func (rl *RateLimiter) Rules() []RateLimitRule {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	out := make([]RateLimitRule, len(rl.rules))
	copy(out, rl.rules)
	return out
}

func (rl *RateLimiter) ruleFor(path string) RateLimitRule {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	for _, r := range rl.rules {
		for _, prefix := range r.PathPrefixes {
			if strings.HasPrefix(path, prefix) {
				return r
			}
		}
	}
	// Default: no specific rule matched - last rule is expected to be a catch-all,
	// but fall back to a conservative default if the caller didn't provide one.
	return RateLimitRule{Name: "default-fallback", Limit: 300, Window: time.Minute}
}

// identify returns the key to rate-limit on: tenant_id once authenticated
// (set by AuthMiddleware, which runs before this middleware in the chain),
// falling back to client IP for unauthenticated requests like /auth/login -
// this is what stops credential-stuffing/brute-force against login itself.
func identify(r *http.Request) string {
	if tenantID := r.Header.Get("X-Tenant-ID"); tenantID != "" {
		return "tenant:" + tenantID
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

// RateLimit enforces the configured rules using an atomic Redis INCR+EXPIRE
// fixed-window counter, so concurrent requests hitting different gateway
// replicas at the same instant still can't exceed the shared budget.
func (rl *RateLimiter) RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rule := rl.ruleFor(r.URL.Path)
		// Premium tenants get a higher budget on the same rule - reuses the tenant
		// tier AuthMiddleware already resolves onto X-Tenant-Tier for billing/SLA.
		if r.Header.Get("X-Tenant-Tier") == "premium" {
			rule.Limit *= 5
		}
		key := fmt.Sprintf("ratelimit:%s:%s:%d", identify(r), routeClass(rule), windowBucket(rule.Window))

		ctx, cancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
		defer cancel()

		count, err := rl.rdb.Incr(ctx, key).Result()
		if err != nil {
			// Redis unavailable: fail open rather than take the whole gateway down.
			log.Printf("ratelimit: redis error, failing open: %v", err)
			next.ServeHTTP(w, r)
			return
		}
		if count == 1 {
			rl.rdb.Expire(ctx, key, rule.Window)
		}
		ttl, _ := rl.rdb.TTL(ctx, key).Result()
		resetAt := time.Now().Add(ttl).Unix()

		remaining := rule.Limit - int(count)
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rule.Limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))

		if int(count) > rule.Limit {
			w.Header().Set("Retry-After", strconv.FormatInt(int64(ttl.Seconds())+1, 10))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "rate limit exceeded",
				"key":   identify(r),
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func routeClass(r RateLimitRule) string {
	if r.Name != "" {
		return r.Name
	}
	if len(r.PathPrefixes) == 0 {
		return "default"
	}
	return r.PathPrefixes[0]
}

// windowBucket assigns requests to a fixed time bucket of the given width,
// e.g. Window=time.Minute -> a new counter key every minute on the minute.
func windowBucket(window time.Duration) int64 {
	if window <= 0 {
		window = time.Minute
	}
	return time.Now().Unix() / int64(window.Seconds())
}
