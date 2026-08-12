package handler

import (
	"strings"
	"testing"
)

func TestClassifyUserAgent(t *testing.T) {
	cases := []struct {
		name                     string
		ua                       string
		browser, os, deviceType  string
	}{
		{
			name:    "Chrome on Windows desktop",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36",
			browser: "Chrome", os: "Windows", deviceType: "Desktop",
		},
		{
			name:    "Safari on iPhone is mobile iOS, not Chrome",
			ua:      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			browser: "Safari", os: "iOS", deviceType: "Mobile",
		},
		{
			name:    "Edge wins over the Chrome token it embeds",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36 Edg/120.0",
			browser: "Edge", os: "Windows", deviceType: "Desktop",
		},
		{
			name:    "Chrome on Android phone is mobile",
			ua:      "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36",
			browser: "Chrome", os: "Android", deviceType: "Mobile",
		},
		{
			name:    "iPad is a tablet",
			ua:      "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/604.1",
			browser: "Safari", os: "iOS", deviceType: "Tablet",
		},
		{
			name:    "Firefox on macOS desktop",
			ua:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:121.0) Gecko/20100101 Firefox/121.0",
			browser: "Firefox", os: "macOS", deviceType: "Desktop",
		},
		{
			name:    "empty UA is all Other",
			ua:      "",
			browser: "Other", os: "Other", deviceType: "Other",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, o, d := classifyUserAgent(c.ua)
			if b != c.browser || o != c.os || d != c.deviceType {
				t.Errorf("classifyUserAgent(%q) = (%q,%q,%q), want (%q,%q,%q)", c.ua, b, o, d, c.browser, c.os, c.deviceType)
			}
		})
	}
}

func TestSortedBreakdown_OrderedByCountThenName(t *testing.T) {
	got := sortedBreakdown(map[string]int64{"Chrome": 10, "Safari": 10, "Firefox": 25})
	// Firefox (25) first; Chrome/Safari tie at 10 → alphabetical.
	want := []string{"Firefox", "Chrome", "Safari"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("position %d = %q, want %q (full: %+v)", i, got[i].Name, w, got)
		}
	}
}

func TestCwvVerdict(t *testing.T) {
	cases := []struct{ metric string; p75 float64; want string }{
		{"LCP", 2000, "good"}, {"LCP", 3000, "needs-improvement"}, {"LCP", 5000, "poor"},
		{"INP", 150, "good"}, {"INP", 300, "needs-improvement"}, {"INP", 600, "poor"},
		{"CLS", 0.05, "good"}, {"CLS", 0.2, "needs-improvement"}, {"CLS", 0.4, "poor"},
		{"FID", 80, "good"}, {"lcp", 2500, "good"}, // boundary + case-insensitive
		{"UNKNOWN", 1, "unknown"},
	}
	for _, c := range cases {
		if got := cwvVerdict(c.metric, c.p75); got != c.want {
			t.Errorf("cwvVerdict(%s, %v) = %s, want %s", c.metric, c.p75, got, c.want)
		}
	}
}

func TestBuildWebVitalsSQL_DimensionAndSafety(t *testing.T) {
	page, dim := buildWebVitalsSQL("page", "24 HOUR")
	if dim != "page" || !strings.Contains(page, "Path AS group_value") {
		t.Fatalf("page dimension should group by Path, got dim=%s", dim)
	}
	dev, ddim := buildWebVitalsSQL("device", "7 DAY")
	if ddim != "device" || !strings.Contains(dev, "'Tablet'") || !strings.Contains(dev, "'Mobile'") || !strings.Contains(dev, "'Desktop'") {
		t.Fatal("device dimension should classify UA into Tablet/Mobile/Desktop in SQL")
	}
	// Unknown dimension defaults to page.
	if _, d := buildWebVitalsSQL("bogus", "24 HOUR"); d != "page" {
		t.Fatalf("unknown dimension should default to page, got %s", d)
	}
	// Always tenant-scoped + web_vitals only.
	for _, sql := range []string{page, dev} {
		if !strings.Contains(sql, "TenantID = {tenant:String}") || !strings.Contains(sql, "Type = 'web_vitals'") {
			t.Fatal("web-vitals query must be tenant-scoped and web_vitals-only")
		}
	}
}
