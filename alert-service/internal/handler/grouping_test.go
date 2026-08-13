package handler

import (
	"testing"
	"time"

	"github.com/pulsetrace/shared/models"
)

func TestFingerprintMessage(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"digits normalized", "timeout after 3200ms", "timeout after #ms"},
		{"uuid normalized", "req 9f2c1a4b-1234-4abc-8def-0123456789ab failed", "req <id> failed"},
		{"hex normalized", "null deref at 0xDEADBEEF", "null deref at <hex>"},
		{"case + whitespace collapsed", "  Connection   REFUSED  ", "connection refused"},
		{"two messages differing only in number collapse", "retry 5 of 10", "retry # of #"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fingerprintMessage(c.in); got != c.want {
				t.Errorf("fingerprintMessage(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestGroupKey_DedupsVolatileDetail(t *testing.T) {
	a := &models.Alert{ServiceName: "payment", Level: "ERROR", Message: "timeout after 3200ms on req 9f2c1a4b-1234-4abc-8def-0123456789ab"}
	b := &models.Alert{ServiceName: "payment", Level: "ERROR", Message: "timeout after 5100ms on req a1b7c2d3-9999-4abc-8def-0123456789ff"}
	if GroupKey(a) != GroupKey(b) {
		t.Fatalf("alerts differing only in id/number should share a key:\n a=%q\n b=%q", GroupKey(a), GroupKey(b))
	}
	// Different service must NOT collapse.
	c := &models.Alert{ServiceName: "checkout", Level: "ERROR", Message: "timeout after 3200ms on req 9f2c1a4b-1234-4abc-8def-0123456789ab"}
	if GroupKey(a) == GroupKey(c) {
		t.Fatal("different service must produce a different group key")
	}
	// Different level must NOT collapse.
	d := &models.Alert{ServiceName: "payment", Level: "WARNING", Message: a.Message}
	if GroupKey(a) == GroupKey(d) {
		t.Fatal("different level must produce a different group key")
	}
}

func TestGroupAlerts(t *testing.T) {
	base := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	alerts := []*models.Alert{
		{ID: "1", ServiceName: "payment", Level: "ERROR", Message: "timeout after 100ms", TriggeredAt: base},
		{ID: "2", ServiceName: "payment", Level: "ERROR", Message: "timeout after 200ms", TriggeredAt: base.Add(2 * time.Minute)},
		{ID: "3", ServiceName: "payment", Level: "ERROR", Message: "timeout after 300ms", TriggeredAt: base.Add(1 * time.Minute)},
		{ID: "4", ServiceName: "checkout", Level: "WARNING", Message: "slow query", TriggeredAt: base.Add(5 * time.Minute)},
		nil, // must be skipped, not panic
	}

	groups := GroupAlerts(alerts)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(groups), groups)
	}

	// Ordered most-recent-first: checkout (last seen +5m) before payment (+2m).
	if groups[0].Service != "checkout" {
		t.Errorf("expected checkout group first (most recent), got %q", groups[0].Service)
	}

	// Find the payment group and assert its aggregation.
	var pay *models.AlertGroup
	for i := range groups {
		if groups[i].Service == "payment" {
			pay = &groups[i]
		}
	}
	if pay == nil {
		t.Fatal("payment group missing")
	}
	if pay.Count != 3 {
		t.Errorf("payment count = %d, want 3", pay.Count)
	}
	if !pay.FirstSeen.Equal(base) {
		t.Errorf("payment first_seen = %v, want %v", pay.FirstSeen, base)
	}
	if !pay.LastSeen.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("payment last_seen = %v, want %v", pay.LastSeen, base.Add(2*time.Minute))
	}
	// Sample is the most recent instance (id "2").
	if pay.SampleID != "2" {
		t.Errorf("payment sample_id = %q, want %q (most recent)", pay.SampleID, "2")
	}
	if len(pay.Instances) != 3 {
		t.Errorf("payment instances = %d, want 3", len(pay.Instances))
	}
}

func TestGroupAlerts_Empty(t *testing.T) {
	if got := GroupAlerts(nil); len(got) != 0 {
		t.Errorf("GroupAlerts(nil) = %+v, want empty", got)
	}
}
