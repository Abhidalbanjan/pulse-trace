// Package sqlq is the query core for user-authored SQL (phase P3.1).
//
// # Why this package exists at all
//
// Every question a user can ask PulseTrace today has to have been anticipated
// by a Go handler. That is the largest single capability gap against
// OpenObserve, and the benchmark measures it: two of six query classes are not
// expressible against us at any latency.
//
// # Why it is security-critical
//
// Opening SQL to users invalidates the threat model the current guard was built
// for. `clickhouse.go`'s assertTenantScoped is a *textual* check — it accepts
// any query whose text contains "TenantID", "tenant.id" or "{tenant:String}"
// anywhere at all. That is sound for server-built SQL, where we control the
// text. It is trivially bypassed by user-authored SQL; all three of these read
// every tenant's data and pass it today:
//
//	SELECT * FROM pulsetrace.rum_events -- TenantID
//	SELECT * FROM pulsetrace.rum_events WHERE Name = 'TenantID'
//	SELECT * FROM pulsetrace.otel_traces /* tenant.id */
//
// So the isolation guarantee here must not rest on inspecting user text. It
// rests on two things, in this order:
//
//  1. **The catalog is an allowlist.** A query may reference only the logical
//     relations named below. There is no syntax for naming a physical table, a
//     system schema, or another tenant's data, because those names do not
//     resolve. Rejection is the default and reachability is the exception.
//  2. **Tenant binding is structural, not syntactic.** A relation is
//     materialised by a scanner that binds the tenant itself; the user's SQL
//     never supplies, and cannot influence, the tenant value. Validation exists
//     to keep the surface small, not to be the thing standing between one
//     tenant and another.
//
// Point 2 is the load-bearing one, and it is deliberately not implemented in
// this file — see the scanner slice. What is here is point 1 plus the policy
// that keeps the accepted grammar small enough to reason about.
package sqlq

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Store identifies which engine backs a logical relation.
type Store string

const (
	StoreLogs      Store = "logs"      // Quickwit
	StoreAnalytics Store = "analytics" // ClickHouse
	StoreMeta      Store = "meta"      // Postgres
)

// Relation is one logical, user-facing relation.
//
// The name a user writes is deliberately *not* the physical table name. Users
// write `logs`, not `pulsetrace.otel_logs`; `traces`, not `otel_traces`. That
// indirection is not cosmetic — it means a query cannot name a physical object
// even when the attacker knows exactly what it is called, and it leaves the
// physical schema free to change under a stable contract.
type Relation struct {
	Name    string
	Store   Store
	Columns []string
	// TenantBound records that rows of this relation exist per tenant and a
	// scanner must therefore bind a tenant before any row is produced. Every
	// relation here is tenant-bound; the field exists so that adding a genuinely
	// global relation later is an explicit, reviewable decision rather than an
	// omission.
	TenantBound bool
	// AttrPrefix declares that this relation carries open-ended, user-supplied
	// attributes addressable as `<prefix>.<key>`.
	//
	// Columns above are a closed set decided by us. Attributes are the opposite:
	// whatever the customer chose to attach to their records, known only at read
	// time. They still cannot name an arbitrary thing — the key must match
	// attrKeyPattern, and a relation without a prefix has no attributes at all —
	// but the set is not enumerable in advance, which is why they are a separate
	// mechanism rather than more entries in Columns.
	AttrPrefix string
}

// attrKeyPattern is what an attribute key may contain.
//
// The key reaches Quickwit as a field name inside a query string, so this is a
// value-becomes-syntax boundary and gets the same treatment as the tenant id:
// an identifier-shaped allowlist, and a refusal rather than an escape for
// anything outside it. Bounded length because a field name is not a payload.
var attrKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// AttrKey returns the attribute key when name addresses one of this relation's
// open-ended attributes, or "" when it does not.
//
// The prefix match is exact and the key keeps its case: attribute keys are the
// customer's spelling, and lowercasing `metadata.customerId` would resolve to a
// field the index does not have and quietly return no rows.
func (r Relation) AttrKey(name string) string {
	if r.AttrPrefix == "" {
		return ""
	}
	rest, ok := strings.CutPrefix(name, r.AttrPrefix+".")
	if !ok || !attrKeyPattern.MatchString(rest) {
		return ""
	}
	return rest
}

// Catalog resolves user-written names to logical relations.
type Catalog struct {
	byName map[string]Relation
}

// DefaultCatalog is the relation set exposed to user SQL.
//
// Additions are a security decision, not a product one: every name added here
// becomes reachable from arbitrary user input. Columns are listed explicitly
// rather than reflected from the store so that a column added upstream is not
// silently exposed — particularly relevant for the ClickHouse tables, which
// carry internal columns (TenantID itself among them) that users must not
// select or filter on directly.
func DefaultCatalog() *Catalog {
	rels := []Relation{
		// Quickwit `pulsetrace-logs`. Field list taken from the live index
		// mapping, not from what a log record "ought" to have — the first draft
		// of this catalog invented a span_id here that the index does not carry,
		// which would have resolved at validation and failed at scan time.
		{
			Name: "logs", Store: StoreLogs, TenantBound: true,
			Columns: []string{"timestamp", "service_name", "level", "message", "trace_id"},
			// Callers attach arbitrary key/values to a log record; they arrive
			// as `metadata` and are addressed as `metadata.<key>`, e.g.
			// `` `metadata.customer_id` `` (backticks because the dot would
			// otherwise parse as a table qualifier). Nested rather than merged
			// into the top level so a customer attribute named `level` cannot
			// shadow the real log level.
			AttrPrefix: "metadata",
		},
		// ClickHouse pulsetrace.otel_traces. Note this table has no TenantID
		// column at all — the tenant lives in ResourceAttributes['tenant.id'],
		// which is why the scanner, not the catalog, owns the tenant predicate.
		{
			Name: "traces", Store: StoreAnalytics, TenantBound: true,
			Columns: []string{"timestamp", "trace_id", "span_id", "parent_span_id", "service_name",
				"span_name", "span_kind", "duration_ms", "status_code", "status_message"},
		},
		// ClickHouse pulsetrace.rum_events.
		{
			Name: "rum_events", Store: StoreAnalytics, TenantBound: true,
			Columns: []string{"timestamp", "session_id", "event_type", "path", "user_agent",
				"metric_name", "metric_value", "error_message", "trace_id", "span_id"},
		},
		// ClickHouse pulsetrace.synthetic_results.
		{
			Name: "synthetic_results", Store: StoreAnalytics, TenantBound: true,
			Columns: []string{"timestamp", "check_name", "url", "status_code", "latency_ms",
				"success", "failure_reason"},
		},
		// Postgres.
		{
			Name: "deployments", Store: StoreMeta, TenantBound: true,
			Columns: []string{"id", "service", "version", "git_sha", "environment", "deployed_by", "deployed_at"},
		},
		{
			Name: "incidents", Store: StoreMeta, TenantBound: true,
			Columns: []string{"id", "title", "status", "severity", "root_cause", "alert_count",
				"started_at", "resolved_at"},
		},

		// Deliberately absent: `metrics`.
		//
		// There is no pulsetrace.otel_metrics table. The collector writes five
		// typed tables — otel_metrics_gauge, _sum, _histogram, _summary and
		// _exponential_histogram — with different shapes, so a single `metrics`
		// relation means choosing a unifying schema and a UNION across all five.
		// That is a real modelling decision, not a mapping, and shipping the name
		// before making it would give users a relation that resolves and then
		// fails. NewEngine refuses to start when a relation has no scanner, which
		// is what turned this from an assumption into a caught error.
	}
	c := &Catalog{byName: make(map[string]Relation, len(rels))}
	for _, r := range rels {
		c.byName[r.Name] = r
	}
	return c
}

// Lookup resolves a user-written relation name.
//
// Matching is case-insensitive on the bare name and rejects any qualified name
// outright. A user may write `logs` or `LOGS`; `pulsetrace.logs`, `system.logs`
// and `db.logs` are all unresolvable, so there is no way to reach across a
// schema boundary even by guessing.
func (c *Catalog) Lookup(schema, name string) (Relation, bool) {
	if schema != "" {
		return Relation{}, false
	}
	r, ok := c.byName[strings.ToLower(name)]
	return r, ok
}

// Names lists the catalog's relations, sorted — used to make the rejection
// message for an unknown relation actionable rather than merely negative.
func (c *Catalog) Names() []string {
	out := make([]string, 0, len(c.byName))
	for n := range c.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// HasColumn reports whether the relation exposes a column, either as one of its
// declared columns or as an open-ended attribute.
func (r Relation) HasColumn(name string) bool {
	for _, c := range r.Columns {
		if strings.EqualFold(c, name) {
			return true
		}
	}
	return r.AttrKey(name) != ""
}

func (r Relation) String() string { return fmt.Sprintf("%s(%s)", r.Name, r.Store) }
