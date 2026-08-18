package kafka

import "testing"

// The classification is the whole safety argument for cutting Kafka retention
// from 168h to 24h, so it is tested at every boundary rather than in the middle
// of each band. The failure that matters is not a mis-sorted warning — it is
// reporting "ok" for a partition that has already lost records.
func TestClassifyRetention(t *testing.T) {
	const risk = DefaultRiskFraction

	cases := []struct {
		name                      string
		oldest, newest, committed int64
		wantState                 RetentionState
		wantLag                   int64
		wantHeadroom              int64
		wantLost                  int64
	}{
		{
			name:   "healthy consumer near the head",
			oldest: 1000, newest: 2000, committed: 1900,
			wantState: RetentionOK, wantLag: 100, wantHeadroom: 900,
		},
		{
			name:   "caught up completely",
			oldest: 1000, newest: 2000, committed: 2000,
			wantState: RetentionOK, wantLag: 0, wantHeadroom: 1000,
		},
		{
			// The case the watchdog exists for: the broker deleted records this
			// group never read. Must never be reported as merely at-risk.
			name:   "committed behind the trailing edge is data loss",
			oldest: 1000, newest: 2000, committed: 940,
			wantState: RetentionDataLost, wantLag: 1060, wantLost: 60,
		},
		{
			name:   "exactly at the trailing edge is not yet loss",
			oldest: 1000, newest: 2000, committed: 1000,
			// headroom 0 of span 1000 → below the 10% risk floor.
			wantState: RetentionAtRisk, wantLag: 1000, wantHeadroom: 0,
		},
		{
			name:   "one record before the trailing edge is loss",
			oldest: 1000, newest: 2000, committed: 999,
			wantState: RetentionDataLost, wantLag: 1001, wantLost: 1,
		},
		{
			// span 1000, risk floor 100: headroom 99 trips, 100 does not.
			name:   "just inside the risk floor",
			oldest: 1000, newest: 2000, committed: 1099,
			wantState: RetentionAtRisk, wantLag: 901, wantHeadroom: 99,
		},
		{
			name:   "exactly on the risk floor is ok",
			oldest: 1000, newest: 2000, committed: 1100,
			wantState: RetentionOK, wantLag: 900, wantHeadroom: 100,
		},
		{
			name:   "never committed is unknown, not loss",
			oldest: 1000, newest: 2000, committed: -1,
			wantState: RetentionUnknown,
		},
		{
			// A fresh topic: nothing retained, so there is no edge to fall off.
			name:   "empty partition is ok",
			oldest: 0, newest: 0, committed: 0,
			wantState: RetentionOK,
		},
		{
			// Seen transiently while metadata refreshes. Must not report a
			// negative lag, which would read as "ahead of the log".
			name:   "committed past the high water mark clamps lag to zero",
			oldest: 1000, newest: 2000, committed: 2050,
			wantState: RetentionOK, wantLag: 0, wantHeadroom: 1050,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, lag, headroom, lost := classifyRetention(tc.oldest, tc.newest, tc.committed, risk)
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
			if lag != tc.wantLag {
				t.Errorf("lag = %d, want %d", lag, tc.wantLag)
			}
			if headroom != tc.wantHeadroom {
				t.Errorf("headroom = %d, want %d", headroom, tc.wantHeadroom)
			}
			if lost != tc.wantLost {
				t.Errorf("lost = %d, want %d", lost, tc.wantLost)
			}
		})
	}
}

// Data loss must be reported for any consumer behind the trailing edge no
// matter how large the partition, so the check can never be diluted by scale.
func TestClassifyRetentionDetectsLossAtAnyScale(t *testing.T) {
	for _, span := range []int64{10, 1_000, 5_104_768, 1 << 40} {
		oldest := span
		newest := oldest + span
		state, _, _, lost := classifyRetention(oldest, newest, oldest-1, DefaultRiskFraction)
		if state != RetentionDataLost {
			t.Errorf("span %d: state = %q, want %q", span, state, RetentionDataLost)
		}
		if lost != 1 {
			t.Errorf("span %d: lost = %d, want 1", span, lost)
		}
	}
}

// A risk fraction of zero disables the early warning but must never suppress
// the data-loss verdict — the two are independent judgements.
func TestClassifyRetentionZeroRiskFractionStillReportsLoss(t *testing.T) {
	if state, _, _, _ := classifyRetention(1000, 2000, 1000, 0); state != RetentionOK {
		t.Errorf("at-edge with zero risk fraction: state = %q, want %q", state, RetentionOK)
	}
	if state, _, _, lost := classifyRetention(1000, 2000, 999, 0); state != RetentionDataLost || lost != 1 {
		t.Errorf("behind edge with zero risk fraction: state = %q lost = %d, want %q 1", state, lost, RetentionDataLost)
	}
}
