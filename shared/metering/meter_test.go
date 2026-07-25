package metering

import "testing"

func TestParseCounterKey(t *testing.T) {
	cases := []struct {
		key                    string
		wantTenant, wantSignal string
		wantOK                 bool
	}{
		{"usage:acme:2026-07-25:traces", "acme", "traces", true},
		{"usage:default:2026-07-25:rum", "default", "rum", true},
		// tenant ids can contain hyphens (from the signup slug + suffix)
		{"usage:acme-inc-9f3a:2026-01-02:logs", "acme-inc-9f3a", "logs", true},
		{"notusage:a:2026-07-25:traces", "", "", false},
		{"usage:a:not-a-date:traces", "", "", false},
		{"usage:a:2026-07-25", "", "", false}, // too few parts
	}
	for _, c := range cases {
		tenant, _, signal, ok := parseCounterKey(c.key)
		if ok != c.wantOK || (ok && (tenant != c.wantTenant || signal != c.wantSignal)) {
			t.Errorf("parseCounterKey(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.key, tenant, signal, ok, c.wantTenant, c.wantSignal, c.wantOK)
		}
	}
}

// A nil / disabled meter must be safe to call (metering must never break ingest).
func TestNilMeterIsSafe(t *testing.T) {
	var m *Meter
	m.Record(nil, "acme", SignalTraces, 5) // must not panic
	if got := m.CurrentUsage(nil, "acme", SignalTraces, "2026-07-25"); got != 0 {
		t.Errorf("nil meter CurrentUsage = %d, want 0", got)
	}

	disabled := New("", nil) // no redis addr → no-op meter
	disabled.Record(nil, "acme", SignalTraces, 5)
	if got := disabled.CurrentUsage(nil, "acme", SignalTraces, "2026-07-25"); got != 0 {
		t.Errorf("disabled meter CurrentUsage = %d, want 0", got)
	}
}
