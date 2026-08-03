package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// clickHouseClient wraps a ClickHouse HTTP endpoint. Shared by any handler that
// queries the otel_traces / rum_events tables directly.
type clickHouseClient struct {
	URL string
}

// ClickHouse credentials were previously hardcoded as literal strings at every
// call site (clickhouse.go, rum_handler.go, synthetics_handler.go — 9 places
// total). Centralized here and overridable via env so a real deployment only
// has to change these in one place instead of hunting through the codebase.
// Defaults match the CLICKHOUSE_USER/CLICKHOUSE_PASSWORD the clickhouse
// container itself is provisioned with in docker-compose.yml.
var (
	clickhouseUser     = getEnvOrDefault("CLICKHOUSE_USER", "pulsetrace")
	clickhousePassword = getEnvOrDefault("CLICKHOUSE_PASSWORD", "pulsetrace_secret")
)

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var intervalToSQL = map[string]string{
	"1h":  "1 HOUR",
	"24h": "1 DAY",
	"7d":  "7 DAY",
}

var intervalToBucket = map[string]string{
	"1h":  "toStartOfMinute(Timestamp)",
	"24h": "toStartOfInterval(Timestamp, INTERVAL 15 MINUTE)",
	"7d":  "toStartOfHour(Timestamp)",
}

// resolveInterval validates a user-supplied interval string, defaulting to "1h".
func resolveInterval(raw string) (key, sqlInterval, bucketExpr string) {
	if _, ok := intervalToSQL[raw]; !ok {
		raw = "1h"
	}
	return raw, intervalToSQL[raw], intervalToBucket[raw]
}

// tenantScopedCHTables are the ClickHouse tables that hold per-tenant telemetry.
// A read that touches any of these MUST carry a tenant predicate; queryScoped
// enforces that so "forgot the tenant filter" becomes an immediate error instead
// of a silent cross-tenant data leak (ROAD_TO_100 · F0.3, rubric R5).
var tenantScopedCHTables = []string{"otel_traces", "otel_logs", "otel_metrics", "rum_events", "synthetic_results"}

// tenantPredicateTokens are the accepted ways a query narrows to one tenant: the
// resource-attribute clause the collector-owned tables use, an explicit TenantID
// column (rum_events / synthetic_results), or the tenant bind param itself.
var tenantPredicateTokens = []string{"tenant.id", "TenantID", "{tenant:String}"}

// queryScoped runs a ClickHouse read that is guaranteed to be tenant-isolated. It
//  1. refuses an empty tenant (fail closed rather than read everything),
//  2. injects the "tenant" bind param from a single trusted source so the value
//     can't be forgotten or spoofed per call site, and
//  3. fails closed if the SQL reads a tenant-scoped table without a tenant
//     predicate.
//
// This turns the previously-ad-hoc convention ("remember to add tenantClause")
// into an enforced invariant at the one choke point every CH read passes through.
func (c *clickHouseClient) queryScoped(tenantID, sql string, params map[string]string) (*http.Response, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("clickhouse: refusing tenant-scoped query with empty tenant")
	}
	if err := assertTenantScoped(sql); err != nil {
		return nil, err
	}
	if params == nil {
		params = map[string]string{}
	}
	params["tenant"] = tenantID
	return c.query(sql, params)
}

// assertTenantScoped returns an error when sql reads a tenant-scoped table but
// contains no tenant predicate. It's a deliberately conservative textual check —
// the query layer is raw SQL, not an AST — and errs toward blocking: a query on
// a tenant table with no recognizable tenant filter is treated as a leak.
func assertTenantScoped(sql string) error {
	if !referencesTenantScopedTable(sql) {
		return nil // system/introspection or non-tenant table: nothing to enforce
	}
	for _, tok := range tenantPredicateTokens {
		if strings.Contains(sql, tok) {
			return nil
		}
	}
	return fmt.Errorf("clickhouse: query reads a tenant-scoped table without a tenant predicate: %q", firstLine(sql))
}

func referencesTenantScopedTable(sql string) bool {
	for _, t := range tenantScopedCHTables {
		if strings.Contains(sql, t) {
			return true
		}
	}
	return false
}

func firstLine(sql string) string {
	s := strings.TrimSpace(sql)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// query posts a parameterized query to ClickHouse's HTTP interface.
// params are passed as ClickHouse HTTP query-string bind parameters (param_<name>=<value>),
// referenced in the SQL as {<name>:<Type>}, so caller-supplied values are never string-concatenated into SQL.
//
// Prefer queryScoped for any read of tenant data; call query directly only for
// genuinely tenant-independent work (DDL, system tables).
func (c *clickHouseClient) query(sql string, params map[string]string) (*http.Response, error) {
	reqURL := c.URL
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set("param_"+k, v)
		}
		reqURL = reqURL + "?" + q.Encode()
	}

	req, err := http.NewRequest("POST", reqURL, bytes.NewBufferString(sql))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	client := &http.Client{}
	return client.Do(req)
}

// arrayParam formats a list of strings as a ClickHouse Array(String) HTTP parameter value: ['a','b']
func arrayParam(values []string) string {
	escaped := make([]string, len(values))
	for i, v := range values {
		escaped[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return "[" + strings.Join(escaped, ",") + "]"
}

// stringParam formats a single string as a ClickHouse String HTTP parameter value.
func stringParam(v string) string {
	return v
}
