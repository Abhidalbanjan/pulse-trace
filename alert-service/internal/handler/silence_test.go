package handler

import (
	"testing"
	"time"

	"github.com/pulsetrace/shared/models"
)

func mkSilence(m models.SilenceMatcher, start, end time.Time) *models.AlertSilence {
	return &models.AlertSilence{Matcher: m, StartsAt: start, EndsAt: end}
}

func TestSilenceMatches(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	alert := &models.Alert{ServiceName: "payment", Level: "ERROR", Message: "connection timeout to db"}

	cases := []struct {
		name    string
		s       *models.AlertSilence
		want    bool
	}{
		{"blanket active window", mkSilence(models.SilenceMatcher{}, past, future), true},
		{"service match", mkSilence(models.SilenceMatcher{Service: "payment"}, past, future), true},
		{"service mismatch", mkSilence(models.SilenceMatcher{Service: "cart"}, past, future), false},
		{"level match (case-insensitive)", mkSilence(models.SilenceMatcher{Level: "error"}, past, future), true},
		{"level mismatch", mkSilence(models.SilenceMatcher{Level: "WARNING"}, past, future), false},
		{"message substring match", mkSilence(models.SilenceMatcher{MessageContains: "timeout"}, past, future), true},
		{"message substring mismatch", mkSilence(models.SilenceMatcher{MessageContains: "disk full"}, past, future), false},
		{"all matchers together", mkSilence(models.SilenceMatcher{Service: "payment", Level: "ERROR", MessageContains: "db"}, past, future), true},
		{"not yet active", mkSilence(models.SilenceMatcher{}, future, future.Add(time.Hour)), false},
		{"already expired", mkSilence(models.SilenceMatcher{}, past.Add(-time.Hour), past), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := silenceMatches(c.s, alert, now); got != c.want {
				t.Errorf("silenceMatches = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAnySilenceMatches(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	win := []*models.AlertSilence{
		mkSilence(models.SilenceMatcher{Service: "cart"}, now.Add(-time.Hour), now.Add(time.Hour)),
		mkSilence(models.SilenceMatcher{Service: "payment"}, now.Add(-time.Hour), now.Add(time.Hour)),
	}
	if !anySilenceMatches(win, &models.Alert{ServiceName: "payment", Level: "ERROR"}, now) {
		t.Error("should match the payment silence")
	}
	if anySilenceMatches(win, &models.Alert{ServiceName: "search", Level: "ERROR"}, now) {
		t.Error("no silence covers search")
	}
	if anySilenceMatches(nil, &models.Alert{ServiceName: "payment"}, now) {
		t.Error("empty silence set matches nothing")
	}
}
