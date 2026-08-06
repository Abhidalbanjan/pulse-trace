package handler

import (
	"strings"
	"testing"
)

func TestEvaluateAssertion_DefaultRequires2xx(t *testing.T) {
	// Zero-value assertion = "any 2xx".
	if ok, _ := evaluateAssertion(200, 10, "", Assertion{}); !ok {
		t.Error("200 should pass the default 2xx assertion")
	}
	if ok, reason := evaluateAssertion(500, 10, "", Assertion{}); ok || !strings.Contains(reason, "2xx") {
		t.Errorf("500 should fail the default assertion, got ok=%v reason=%q", ok, reason)
	}
	if ok, reason := evaluateAssertion(0, 0, "", Assertion{}); ok || !strings.Contains(reason, "no response") {
		t.Errorf("a network failure (status 0) must fail, got ok=%v reason=%q", ok, reason)
	}
}

func TestEvaluateAssertion_ExactStatus(t *testing.T) {
	a := Assertion{Status: 404}
	if ok, _ := evaluateAssertion(404, 10, "", a); !ok {
		t.Error("an explicit 404 expectation should pass on a 404 (e.g. a delete-then-verify check)")
	}
	if ok, reason := evaluateAssertion(200, 10, "", a); ok || !strings.Contains(reason, "expected status 404") {
		t.Errorf("200 should fail a status=404 assertion, got ok=%v reason=%q", ok, reason)
	}
}

func TestEvaluateAssertion_MaxLatency(t *testing.T) {
	a := Assertion{MaxLatencyMs: 500}
	if ok, _ := evaluateAssertion(200, 499, "", a); !ok {
		t.Error("latency under the SLA should pass")
	}
	if ok, reason := evaluateAssertion(200, 501, "", a); ok || !strings.Contains(reason, "SLA") {
		t.Errorf("latency over the SLA should fail, got ok=%v reason=%q", ok, reason)
	}
}

func TestEvaluateAssertion_BodyContains(t *testing.T) {
	a := Assertion{BodyContains: `"status":"ok"`}
	if ok, _ := evaluateAssertion(200, 10, `{"status":"ok"}`, a); !ok {
		t.Error("a body containing the substring should pass")
	}
	if ok, reason := evaluateAssertion(200, 10, `{"status":"degraded"}`, a); ok || !strings.Contains(reason, "did not contain") {
		t.Errorf("a body missing the substring should fail, got ok=%v reason=%q", ok, reason)
	}
}

func TestEvaluateAssertion_StatusCheckedBeforeLatency(t *testing.T) {
	// A 500 that is also slow should report the status problem (the root cause),
	// not the latency — status is checked first.
	a := Assertion{MaxLatencyMs: 10}
	ok, reason := evaluateAssertion(500, 9999, "", a)
	if ok || !strings.Contains(reason, "2xx") {
		t.Errorf("expected the status failure to be reported first, got ok=%v reason=%q", ok, reason)
	}
}

func TestValidateProbeURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"public https", "https://example.com/health", false},
		{"public http with path", "http://api.acme.io/status", false},
		{"public ip", "http://93.184.216.34/", false},

		// SSRF vectors that must be rejected.
		{"cloud metadata endpoint", "http://169.254.169.254/latest/meta-data/", true},
		{"localhost name", "http://localhost:8080/", true},
		{"loopback ip", "http://127.0.0.1/", true},
		{"private 10.x", "http://10.0.0.5/", true},
		{"private 192.168.x", "http://192.168.1.1/admin", true},
		{"private 172.16.x", "https://172.16.0.9/", true},
		{"ipv6 loopback", "http://[::1]/", true},
		{"unspecified", "http://0.0.0.0/", true},

		// Wrong scheme / malformed.
		{"file scheme", "file:///etc/passwd", true},
		{"gopher scheme", "gopher://evil/", true},
		{"no scheme", "example.com", true},
		{"empty", "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateProbeURL(c.url)
			if (err != nil) != c.wantErr {
				t.Errorf("validateProbeURL(%q) error = %v, wantErr %v", c.url, err, c.wantErr)
			}
		})
	}
}
