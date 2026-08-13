package handler

// Alert grouping & deduplication (Alerts · E1).
//
// The raw alert stream can bury an operator: one failing dependency easily
// fires the "same" alert hundreds of times, differing only in a request id or a
// latency number. Grouping collapses those into a single row with a count and a
// first/last-seen span, so the list reads as distinct problems rather than a
// firehose. All logic here is pure so it can be unit-tested and reused by any
// caller; the HTTP layer just fetches a page of alerts and hands them in.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/pulsetrace/shared/models"
)

var (
	// Ordered widest-match-first so a UUID isn't half-eaten by the number rule.
	reFingerprintUUID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	reFingerprintHex  = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	reFingerprintNum  = regexp.MustCompile(`\d+`)
	reFingerprintWS   = regexp.MustCompile(`\s+`)
)

// fingerprintMessage normalizes an alert message into a stable signature so two
// alerts that differ only in volatile detail (ids, counts, durations, hex
// addresses) collapse together. e.g. both
//
//	"timeout after 3200ms on req 9f2c…"  and
//	"timeout after 5100ms on req a1b7…"
//
// map to "timeout after #ms on req <id>".
func fingerprintMessage(msg string) string {
	s := strings.ToLower(strings.TrimSpace(msg))
	s = reFingerprintUUID.ReplaceAllString(s, "<id>")
	s = reFingerprintHex.ReplaceAllString(s, "<hex>")
	s = reFingerprintNum.ReplaceAllString(s, "#")
	s = reFingerprintWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// GroupKey is the dedup identity of an alert: same service, same severity, same
// message shape. The alert model carries no rule/label fields, so the fingerprint
// of the message is what distinguishes one class of failure from another.
func GroupKey(a *models.Alert) string {
	return a.ServiceName + "\x1f" + string(a.Level) + "\x1f" + fingerprintMessage(a.Message)
}

// GroupAlerts collapses a slice of alerts into groups by GroupKey. Within a
// group the representative Sample is the most recent instance (what an operator
// most likely wants to read), Count is the number of members, and
// FirstSeen/LastSeen bracket the storm. Groups are returned most-recent-first,
// ties broken by larger count, so the loudest active problems surface at the top.
// nil alerts are skipped; input is not mutated.
func GroupAlerts(alerts []*models.Alert) []models.AlertGroup {
	byKey := make(map[string]*models.AlertGroup, len(alerts))
	order := make([]string, 0, len(alerts))

	for _, a := range alerts {
		if a == nil {
			continue
		}
		k := GroupKey(a)
		g, ok := byKey[k]
		if !ok {
			g = &models.AlertGroup{
				Key:       k,
				Service:   a.ServiceName,
				Level:     a.Level,
				Sample:    a.Message,
				SampleID:  a.ID,
				FirstSeen: a.TriggeredAt,
				LastSeen:  a.TriggeredAt,
			}
			byKey[k] = g
			order = append(order, k)
		}
		g.Count++
		g.Instances = append(g.Instances, a)
		if a.TriggeredAt.Before(g.FirstSeen) {
			g.FirstSeen = a.TriggeredAt
		}
		// On a tie keep the first-seen-in-input as representative (stable).
		if a.TriggeredAt.After(g.LastSeen) {
			g.LastSeen = a.TriggeredAt
			g.Sample = a.Message
			g.SampleID = a.ID
		}
	}

	out := make([]models.AlertGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].Count > out[j].Count
	})
	return out
}
