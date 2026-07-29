package ingestproxy

import "time"

// normalizeEpochNanos converts an epoch timestamp of *unknown* unit to
// nanoseconds by its magnitude, then guards against implausible values.
//
// Migration sources disagree on the unit: the Datadog logs intake nominally
// sends epoch-millis, DD metrics epoch-seconds, and Splunk HEC epoch-seconds —
// but real-world agents and forwarders are frequently misconfigured and send a
// different unit (seconds where we expect millis, or millis where we expect
// seconds). Trusting the nominal unit then places the event decades away (a
// seconds value read as millis lands in ~1970; a millis value read as seconds
// lands in the far future), which is exactly what makes migrated telemetry
// vanish from the default "recent" query window. Deciding the unit from the
// value's magnitude is robust to that mismatch — the ranges for s/ms/µs/ns
// don't overlap for any realistic wall-clock time.
//
//	< 1e11  → seconds      (up to ~year 5138 in seconds)
//	< 1e14  → milliseconds
//	< 1e17  → microseconds
//	else    → nanoseconds
//
// A non-positive value returns 0, meaning "unset" — callers fall back to
// observed/now. A value that still resolves to an absurd future (> ~2 days
// ahead, i.e. beyond any sane clock skew) is treated as garbage and also
// returned as 0, so it defaults to now rather than sorting past the window.
// Genuinely old timestamps (legitimate backfill) are preserved as-is.
func normalizeEpochNanos(v float64) uint64 {
	if v <= 0 {
		return 0
	}
	var nanos float64
	switch {
	case v < 1e11:
		nanos = v * 1e9
	case v < 1e14:
		nanos = v * 1e6
	case v < 1e17:
		nanos = v * 1e3
	default:
		nanos = v
	}
	if nanos > maxPlausibleNanos() {
		return 0
	}
	return uint64(nanos)
}

// nowNanos is a package var so tests can pin "now"; defaults to the real clock.
var nowNanos = func() int64 { return time.Now().UnixNano() }

// maxPlausibleNanos is now + ~2 days of allowed clock skew, in nanos.
func maxPlausibleNanos() float64 {
	const twoDaysNanos = 2 * 24 * float64(time.Hour) / float64(time.Nanosecond)
	return float64(nowNanos()) + twoDaysNanos
}
