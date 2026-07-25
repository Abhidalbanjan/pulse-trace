package otlp

import (
	"testing"

	"google.golang.org/protobuf/proto"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// TestStampPayloadProtobufRoundTrip ensures the HTTP path decodes an OTLP
// protobuf body, stamps the tenant onto the resource, and re-encodes a valid
// payload — overriding any client-supplied tenant.id in the process.
func TestStampPayloadProtobufRoundTrip(t *testing.T) {
	in := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				{Key: TenantIDAttr, Value: stringValue("spoofed")},
			}},
		}},
	}
	data, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, err := stampPayload(SignalTraces, data, false, "real-tenant", "premium")
	if err != nil {
		t.Fatalf("stampPayload: %v", err)
	}

	got := &coltracepb.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(out, got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	attrs := got.GetResourceSpans()[0].GetResource().GetAttributes()
	if v, n := attrValue(attrs, TenantIDAttr); v != "real-tenant" || n != 1 {
		t.Errorf("tenant.id = %q (count %d), want real-tenant (count 1)", v, n)
	}
}
