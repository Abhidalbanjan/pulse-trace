package handler

import "testing"

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
