package handler

import (
	"strings"
	"testing"
)

func TestSummarizeToolResult(t *testing.T) {
	if got := summarizeToolResult("  line one\n\tline two   three "); got != "line one line two three" {
		t.Errorf("whitespace not collapsed: %q", got)
	}
	if got := summarizeToolResult(""); got != "(no data returned)" {
		t.Errorf("empty should be placeholder, got %q", got)
	}
	long := strings.Repeat("x", 300)
	got := summarizeToolResult(long)
	if len([]rune(got)) > 181 || !strings.HasSuffix(got, "…") {
		t.Errorf("long result should be truncated with ellipsis, got len %d", len([]rune(got)))
	}
}

func TestToolDeepLink(t *testing.T) {
	if got := toolDeepLink("search_traces", map[string]string{"service": "payment"}); got != "/traces?service=payment" {
		t.Errorf("traces deep link = %q", got)
	}
	l := toolDeepLink("search_logs", map[string]string{"q": "error", "service": "cart"})
	if !strings.HasPrefix(l, "/explorer?") || !strings.Contains(l, "q=error") || !strings.Contains(l, "service=cart") {
		t.Errorf("logs deep link = %q", l)
	}
	if got := toolDeepLink("query_metric", map[string]string{"metric": "x"}); got != "/metrics" {
		t.Errorf("metric deep link = %q", got)
	}
	if got := toolDeepLink("unknown_tool", nil); got != "" {
		t.Errorf("unknown tool should have no deep link, got %q", got)
	}
	// Logs with no args still resolves to the base screen.
	if got := toolDeepLink("search_logs", map[string]string{}); got != "/explorer" {
		t.Errorf("bare logs deep link = %q", got)
	}
}
