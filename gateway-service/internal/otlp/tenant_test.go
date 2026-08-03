package otlp

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// fakeResolver resolves exactly one known key. scope defaults to "ingest" when
// unset so existing cases keep exercising the server-telemetry path.
type fakeResolver struct {
	key    string
	tenant string
	tier   string
	scope  string
}

func (f fakeResolver) Resolve(_ context.Context, plaintext string) (string, string, string, bool) {
	if plaintext != "" && plaintext == f.key {
		scope := f.scope
		if scope == "" {
			scope = scopeIngest
		}
		return f.tenant, f.tier, scope, true
	}
	return "", "", "", false
}

func attrValue(attrs []*commonpb.KeyValue, key string) (string, int) {
	var val string
	count := 0
	for _, kv := range attrs {
		if kv.Key == key {
			count++
			val = kv.GetValue().GetStringValue()
		}
	}
	return val, count
}

// TestStampResourceOverridesSpoofedTenant is the core anti-spoof guard: a
// client that pre-sets tenant.id on the resource must have it replaced by the
// server-resolved tenant, exactly once, with unrelated attributes preserved.
func TestStampResourceOverridesSpoofedTenant(t *testing.T) {
	res := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		{Key: "service.name", Value: stringValue("checkout")},
		{Key: TenantIDAttr, Value: stringValue("attacker-tenant")},
	}}

	out := stampResource(res, "real-tenant", "premium")

	if v, n := attrValue(out.Attributes, TenantIDAttr); v != "real-tenant" || n != 1 {
		t.Errorf("tenant.id = %q (count %d), want real-tenant (count 1)", v, n)
	}
	if v, _ := attrValue(out.Attributes, TenantTierAttr); v != "premium" {
		t.Errorf("tenant.tier = %q, want premium", v)
	}
	if v, _ := attrValue(out.Attributes, "service.name"); v != "checkout" {
		t.Errorf("service.name should be preserved, got %q", v)
	}
}

func TestStampResourceNil(t *testing.T) {
	out := stampResource(nil, "t1", "standard")
	if out == nil {
		t.Fatal("stampResource(nil) must return a non-nil resource")
	}
	if v, _ := attrValue(out.Attributes, TenantIDAttr); v != "t1" {
		t.Errorf("tenant.id = %q, want t1", v)
	}
}

func TestAuthTenantResolvesKey(t *testing.T) {
	s := &tenantStamper{resolver: fakeResolver{key: "good", tenant: "tA", tier: "premium"}, requireKey: true}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer good"))

	tid, tier, err := s.authTenant(ctx)
	if err != nil || tid != "tA" || tier != "premium" {
		t.Errorf("authTenant = (%q,%q,%v), want (tA,premium,nil)", tid, tier, err)
	}
}

func TestAuthTenantRejectsRumScopedKeyOnGRPC(t *testing.T) {
	// A public RUM token must not be usable to write server telemetry via gRPC.
	s := &tenantStamper{resolver: fakeResolver{key: "rumkey", tenant: "tA", tier: "standard", scope: "rum"}, requireKey: false}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer rumkey"))
	if _, _, err := s.authTenant(ctx); status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied for a rum-scoped key on gRPC, got %v", err)
	}
}

func TestAuthTenantRejectsWhenRequired(t *testing.T) {
	s := &tenantStamper{resolver: fakeResolver{key: "good"}, requireKey: true}
	// No authorization metadata at all.
	_, _, err := s.authTenant(context.Background())
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated when key required and absent, got %v", err)
	}
}

func TestAuthTenantDefaultsWhenNotRequired(t *testing.T) {
	s := &tenantStamper{resolver: fakeResolver{key: "good"}, requireKey: false}
	tid, tier, err := s.authTenant(context.Background())
	if err != nil || tid != defaultTenantID || tier != defaultTenantTier {
		t.Errorf("authTenant = (%q,%q,%v), want (default,standard,nil)", tid, tier, err)
	}
}

func TestBearerFromMetadata(t *testing.T) {
	cases := map[string]struct {
		md   metadata.MD
		want string
	}{
		"valid":     {metadata.Pairs("authorization", "Bearer abc"), "abc"},
		"no bearer": {metadata.Pairs("authorization", "abc"), ""},
		"empty":     {metadata.MD{}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), tc.md)
			if got := bearerFromMetadata(ctx); got != tc.want {
				t.Errorf("bearerFromMetadata = %q, want %q", got, tc.want)
			}
		})
	}
}

// When a log sink is wired, emitLogs must route to it (Kafka → Quickwit) and not
// touch the upstream collector client — stamping and metering still happen.
func TestEmitLogs_RoutesToSinkWhenSet(t *testing.T) {
	var metered int64
	var sunkTenant, sunkTier string
	var sunkReq *collogspb.ExportLogsServiceRequest
	s := &tenantStamper{
		record: func(_ context.Context, _, signal string, n int64) {
			if signal == "logs" {
				metered += n
			}
		},
		logSink: func(_ context.Context, tenantID, tier string, req *collogspb.ExportLogsServiceRequest) error {
			sunkTenant, sunkTier, sunkReq = tenantID, tier, req
			return nil
		},
		// logsUp deliberately nil: if emitLogs tried to forward to the collector
		// it would panic, proving the sink path is taken.
	}
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{}, {}},
			}},
		}},
	}
	if err := s.emitLogs(context.Background(), "acme", "premium", req); err != nil {
		t.Fatalf("emitLogs: %v", err)
	}
	if sunkTenant != "acme" || sunkTier != "premium" || sunkReq == nil {
		t.Errorf("sink not called with tenant: %q/%q req=%v", sunkTenant, sunkTier, sunkReq)
	}
	if metered != 2 {
		t.Errorf("expected 2 metered log records, got %d", metered)
	}
	// Tenant stamped onto the resource.
	attrs := sunkReq.GetResourceLogs()[0].GetResource().GetAttributes()
	var sawTenant bool
	for _, a := range attrs {
		if a.GetKey() == TenantIDAttr && a.GetValue().GetStringValue() == "acme" {
			sawTenant = true
		}
	}
	if !sawTenant {
		t.Error("tenant.id not stamped onto the resource before the sink")
	}
}
