package handler

import "testing"

func TestNiceCeil(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{0.4, 0.5}, {1, 1}, {1.1, 2}, {2, 2}, {3, 5}, {5, 5}, {6, 10}, {37, 50}, {150, 200}, {0, 1},
	}
	for _, c := range cases {
		if got := niceCeil(c.in); got != c.want {
			t.Errorf("niceCeil(%g) = %g, want %g", c.in, got, c.want)
		}
	}
}

func TestBucketConfig(t *testing.T) {
	// ~30 buckets over 1000ms → nice width 50 (1000/30≈33 → niceCeil 50), 20 buckets.
	w, n := bucketConfig(1000, 30)
	if w != 50 || n != 20 {
		t.Errorf("bucketConfig(1000,30) = (%g,%d), want (50,20)", w, n)
	}
	// Degenerate inputs are safe.
	if w, n := bucketConfig(0, 30); w != 1 || n != 1 {
		t.Errorf("zero max = (%g,%d), want (1,1)", w, n)
	}
	if w, n := bucketConfig(500, 0); w <= 0 || n < 1 {
		t.Errorf("zero targetBuckets should not divide-by-zero: (%g,%d)", w, n)
	}
}

func TestAssembleLatencyBuckets(t *testing.T) {
	got := assembleLatencyBuckets(map[int]int64{0: 5, 2: 3}, 10, 4)
	if len(got) != 4 {
		t.Fatalf("want 4 contiguous buckets, got %d", len(got))
	}
	if got[0].Count != 5 || got[0].LowerMs != 0 || got[0].UpperMs != 10 {
		t.Errorf("bucket 0 wrong: %+v", got[0])
	}
	if got[1].Count != 0 { // gap must be zero-filled
		t.Errorf("bucket 1 should be zero-filled, got %d", got[1].Count)
	}
	if got[2].Count != 3 || got[2].LowerMs != 20 {
		t.Errorf("bucket 2 wrong: %+v", got[2])
	}
}
