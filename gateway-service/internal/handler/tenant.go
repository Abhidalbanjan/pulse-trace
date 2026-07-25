package handler

import "net/http"

// tenantClause is the ClickHouse WHERE fragment that scopes a query on
// otel_traces / otel_metrics_* to a single tenant. It matches the `tenant.id`
// resource attribute that the gateway's in-process OTLP receiver stamps onto
// every export (see internal/otlp) and the collector persists into the
// ResourceAttributes map column. Always pair it with the "tenant" bind param so
// the value is never string-concatenated into SQL.
const tenantClause = "ResourceAttributes['tenant.id'] = {tenant:String}"

// tenantFromRequest returns the caller's tenant, taken solely from the
// gateway-verified X-Tenant-ID header (set by AuthMiddleware from the JWT or the
// resolved ingestion key — never from an unverified client header). Defaults to
// "default" so single-tenant/dev stacks keep working.
func tenantFromRequest(r *http.Request) string {
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return "default"
}
