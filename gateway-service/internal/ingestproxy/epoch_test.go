package ingestproxy

import "testing"

func TestNormalizeEpochNanos(t *testing.T) {
	// Pin "now" to 2023-11-14T22:13:20Z (epoch 1_700_000_000s) so the future
	// clamp is deterministic.
	const nowSec = int64(1_700_000_000)
	orig := nowNanos
	nowNanos = func() int64 { return nowSec * 1e9 }
	defer func() { nowNanos = orig }()

	const wantNanos = uint64(1_700_000_000) * 1e9 // the same instant in nanos

	cases := []struct {
		name string
		in   float64
		want uint64
	}{
		{"seconds", 1_700_000_000, wantNanos},
		{"milliseconds", 1_700_000_000_000, wantNanos},
		{"microseconds", 1_700_000_000_000_000, wantNanos},
		{"nanoseconds", 1_700_000_000_000_000_000, wantNanos},
		{"fractional seconds", 1_700_000_000.5, wantNanos + 5e8},
		{"zero is unset", 0, 0},
		{"negative is unset", -1, 0},
		// A millis value mistakenly declared as seconds would land ~55000 CE.
		// Magnitude detection reads it as millis instead, recovering the instant.
		{"unit mismatch recovered", 1_700_000_000_000, wantNanos},
	}
	for _, c := range cases {
		if got := normalizeEpochNanos(c.in); got != c.want {
			t.Errorf("%s: normalizeEpochNanos(%g) = %d, want %d", c.name, c.in, got, c.want)
		}
	}

	// A nanos value 10 days in the future (beyond the ~2-day skew) is garbage →
	// unset, so it defaults to now rather than sorting past the query window.
	tenDaysAheadNanos := float64(nowSec+10*86400) * 1e9
	if got := normalizeEpochNanos(tenDaysAheadNanos); got != 0 {
		t.Errorf("far-future timestamp should be treated as unset, got %d", got)
	}

	// A genuinely old (backfill) timestamp is preserved, not rewritten to now.
	oldSec := float64(nowSec - 30*86400) // 30 days ago
	if got := normalizeEpochNanos(oldSec); got != uint64(oldSec)*1e9 {
		t.Errorf("legitimate backfill timestamp should be preserved, got %d", got)
	}
}
