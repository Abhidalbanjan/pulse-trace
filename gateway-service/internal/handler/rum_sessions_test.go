package handler

import "testing"

func TestAssembleSessionTimeline(t *testing.T) {
	raw := []SessionEventInput{
		// Deliberately out of order — assembly must sort by time.
		{Type: "error", ErrorMsg: "TypeError: x is undefined", TraceID: "abc", Timestamp: "2026-08-13 10:00:05"},
		{Type: "page_view", Path: "/checkout", Timestamp: "2026-08-13 10:00:00"},
		{Type: "web_vitals", MetricName: "LCP", MetricValue: 2400, Timestamp: "2026-08-13 10:00:02"},
	}
	tl := assembleSessionTimeline("sess-1", raw)

	if tl.SessionID != "sess-1" || tl.EventCount != 3 {
		t.Fatalf("summary wrong: %+v", tl)
	}
	if len(tl.Events) != 3 {
		t.Fatalf("want 3 events, got %d", len(tl.Events))
	}
	// Sorted: page_view (t0), web_vitals (t2s), error (t5s).
	if tl.Events[0].Kind != "page_view" || tl.Events[1].Kind != "web_vitals" || tl.Events[2].Kind != "error" {
		t.Errorf("events not ordered by time: %+v", tl.Events)
	}
	// Offsets relative to start.
	if tl.Events[0].OffsetMs != 0 || tl.Events[1].OffsetMs != 2000 || tl.Events[2].OffsetMs != 5000 {
		t.Errorf("offsets wrong: %d/%d/%d", tl.Events[0].OffsetMs, tl.Events[1].OffsetMs, tl.Events[2].OffsetMs)
	}
	if tl.DurationMs != 5000 {
		t.Errorf("duration = %d, want 5000", tl.DurationMs)
	}
	if tl.PageViews != 1 || tl.Errors != 1 {
		t.Errorf("counts wrong: pageViews=%d errors=%d", tl.PageViews, tl.Errors)
	}
	// Labels.
	if tl.Events[0].Label != "Viewed /checkout" {
		t.Errorf("page_view label = %q", tl.Events[0].Label)
	}
	if tl.Events[1].Label != "LCP = 2400ms" {
		t.Errorf("web_vitals label = %q", tl.Events[1].Label)
	}
}

func TestAssembleSessionTimeline_Empty(t *testing.T) {
	tl := assembleSessionTimeline("s", nil)
	if tl.EventCount != 0 || len(tl.Events) != 0 || tl.DurationMs != 0 {
		t.Errorf("empty session should be zero, got %+v", tl)
	}
}

func TestFormatVitalValue(t *testing.T) {
	if got := formatVitalValue("CLS", 0.123); got != "0.123" {
		t.Errorf("CLS should be unitless: %q", got)
	}
	if got := formatVitalValue("LCP", 2400); got != "2400ms" {
		t.Errorf("LCP should be ms: %q", got)
	}
}
