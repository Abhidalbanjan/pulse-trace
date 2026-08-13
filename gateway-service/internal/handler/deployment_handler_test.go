package handler

import (
	"testing"
	"time"
)

func TestParseDeployTime(t *testing.T) {
	if _, ok := parseDeployTime(""); ok {
		t.Error("empty string should be 'no bound' (ok=false)")
	}
	if _, ok := parseDeployTime("not-a-time"); ok {
		t.Error("malformed value should be ignored (ok=false), not fail the request")
	}
	got, ok := parseDeployTime("2026-08-12T10:00:00Z")
	if !ok {
		t.Fatal("valid RFC3339 should parse")
	}
	if !got.Equal(time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected parsed time: %v", got)
	}
}
