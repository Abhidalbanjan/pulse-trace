package handler

import (
	"testing"
	"time"
)

func TestClassifyErrorGroup(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	window := 15 * time.Minute
	resolvedAt := now.Add(-2 * time.Hour)

	cases := []struct {
		name       string
		status     string
		resolvedAt time.Time
		firstSeen  time.Time
		lastSeen   time.Time
		want       string
	}{
		{
			name:       "resolved group recurring after resolution is a regression",
			status:     "resolved",
			resolvedAt: resolvedAt,
			firstSeen:  now.Add(-3 * time.Hour),
			lastSeen:   now.Add(-1 * time.Minute), // after resolvedAt
			want:       "regression",
		},
		{
			name:       "resolved group with no occurrences since resolution stays quiet",
			status:     "resolved",
			resolvedAt: resolvedAt,
			firstSeen:  now.Add(-3 * time.Hour),
			lastSeen:   now.Add(-3 * time.Hour), // before resolvedAt
			want:       "",
		},
		{
			name:      "brand-new group first seen inside the window pages as new",
			status:    "",
			firstSeen: now.Add(-5 * time.Minute),
			lastSeen:  now,
			want:      "new",
		},
		{
			name:      "old untriaged group must not page (would flood on boot)",
			status:    "",
			firstSeen: now.Add(-2 * time.Hour), // outside the window
			lastSeen:  now,
			want:      "",
		},
		{
			name:      "muted group never pages",
			status:    "muted",
			firstSeen: now.Add(-1 * time.Minute),
			lastSeen:  now,
			want:      "",
		},
		{
			name:      "already-open group is known, no page",
			status:    "open",
			firstSeen: now.Add(-1 * time.Minute),
			lastSeen:  now,
			want:      "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyErrorGroup(c.status, c.resolvedAt, c.firstSeen, c.lastSeen, now, window)
			if got != c.want {
				t.Errorf("classifyErrorGroup = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFingerprintIsTenantScoped(t *testing.T) {
	// Two tenants sharing an identical error must not collide onto one group —
	// otherwise one tenant's triage state (and the timeline endpoint's integrity
	// check) would leak across the boundary.
	a := fingerprint("tenant-a", "svc", "op", "boom")
	b := fingerprint("tenant-b", "svc", "op", "boom")
	if a == b {
		t.Errorf("fingerprints must differ across tenants, both were %q", a)
	}
	if a != fingerprint("tenant-a", "svc", "op", "boom") {
		t.Error("fingerprint must be stable for the same identity")
	}
}

func TestParseCHTime(t *testing.T) {
	if got := parseCHTime("2026-08-06 12:00:00.000"); got.IsZero() {
		t.Error("millisecond form should parse")
	}
	if got := parseCHTime("2026-08-06 12:00:00"); got.IsZero() {
		t.Error("second form should parse")
	}
	if got := parseCHTime("not a time"); !got.IsZero() {
		t.Error("garbage should yield the zero time (conservative: never a spurious page)")
	}
}
