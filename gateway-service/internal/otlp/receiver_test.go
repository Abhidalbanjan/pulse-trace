package otlp

import "testing"

func TestNormalizeGRPCTarget(t *testing.T) {
	cases := map[string]string{
		// Bare host:port (the DD/Splunk forward target) gets a dns scheme so grpc
		// doesn't misread the hostname as a URI scheme.
		"otel-collector:4317": "dns:///otel-collector:4317",
		"localhost:4317":      "dns:///localhost:4317",
		"10.0.0.5:4317":       "dns:///10.0.0.5:4317",
		// Already-schemed targets are left alone.
		"dns:///otel-collector:4317": "dns:///otel-collector:4317",
		"passthrough:///x:4317":      "passthrough:///x:4317",
		"unix:///tmp/otel.sock":      "unix:///tmp/otel.sock",
		"":                           "",
	}
	for in, want := range cases {
		if got := normalizeGRPCTarget(in); got != want {
			t.Errorf("normalizeGRPCTarget(%q) = %q, want %q", in, got, want)
		}
	}
}
