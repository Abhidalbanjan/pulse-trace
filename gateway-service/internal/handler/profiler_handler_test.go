package handler

import "testing"

// synthetic flamebearer: root(total) → hotFn(self 60), coldFn(self 40).
func sampleFlame() ([]string, [][]int) {
	names := []string{"total", "main", "hotFn", "coldFn"}
	levels := [][]int{
		{0, 100, 0, 0},           // root "total", self 0
		{0, 60, 60, 2, 0, 40, 40, 3}, // hotFn self 60, coldFn self 40
	}
	return names, levels
}

func TestAggregateSelf(t *testing.T) {
	names, levels := sampleFlame()
	self := aggregateSelf(names, levels)
	if self["hotFn"] != 60 || self["coldFn"] != 40 {
		t.Fatalf("aggregateSelf wrong: %+v", self)
	}
	if self["total"] != 0 {
		t.Errorf("root should contribute 0 self, got %d", self["total"])
	}
	if got := sumSelf(self); got != 100 {
		t.Errorf("sumSelf = %d, want 100", got)
	}
}

func TestAggregateSelf_IgnoresOutOfRangeNameIndex(t *testing.T) {
	// A malformed level referencing a nonexistent name index must not panic or
	// mis-attribute — it's silently skipped.
	self := aggregateSelf([]string{"a"}, [][]int{{0, 10, 10, 9}})
	if len(self) != 0 {
		t.Errorf("expected no attribution for a bad name index, got %+v", self)
	}
}

func TestTopFunctions_RanksBySelfAndSkipsRoot(t *testing.T) {
	names, levels := sampleFlame()
	top := topFunctions(aggregateSelf(names, levels), 100, 10)
	if len(top) != 2 {
		t.Fatalf("expected 2 functions (root skipped), got %d: %+v", len(top), top)
	}
	if top[0].Name != "hotFn" || top[0].Self != 60 || top[0].Pct != 60 {
		t.Errorf("hottest function wrong: %+v", top[0])
	}
	if top[1].Name != "coldFn" {
		t.Errorf("expected coldFn second, got %s", top[1].Name)
	}
}

func TestDiffProfiles_DetectsRegressionByShare(t *testing.T) {
	// hotFn's share grows 60%→90% (+30pp regression); coldFn shrinks 40%→10%.
	base := map[string]int64{"hotFn": 60, "coldFn": 40}
	comp := map[string]int64{"hotFn": 90, "coldFn": 10}
	diffs := diffProfiles(base, comp, 100, 100, 1.0, 10)

	if len(diffs) != 2 {
		t.Fatalf("expected 2 diffs, got %d", len(diffs))
	}
	// Largest share increase first.
	if diffs[0].Name != "hotFn" || !diffs[0].Regression {
		t.Errorf("hotFn should be the top regression: %+v", diffs[0])
	}
	if diffs[0].DeltaPct != 30 {
		t.Errorf("hotFn delta should be +30pp, got %v", diffs[0].DeltaPct)
	}
	if diffs[1].Name != "coldFn" || diffs[1].Regression {
		t.Errorf("coldFn should be an improvement, not a regression: %+v", diffs[1])
	}
}

func TestDiffProfiles_NormalizesUniformLoadIncrease(t *testing.T) {
	// Load doubles but the SHARE of each function is unchanged → no regressions.
	base := map[string]int64{"a": 50, "b": 50}
	comp := map[string]int64{"a": 100, "b": 100}
	diffs := diffProfiles(base, comp, 100, 200, 1.0, 10)
	for _, d := range diffs {
		if d.Regression {
			t.Errorf("a uniform load increase must not flag %q as a regression (delta %v)", d.Name, d.DeltaPct)
		}
	}
}

func TestBuildProfilerQuery(t *testing.T) {
	if got := buildProfilerQuery("gateway-service", "process_cpu", ""); got != "gateway-service.process_cpu{}" {
		t.Errorf("plain query wrong: %q", got)
	}
	if got := buildProfilerQuery("gateway-service", "process_cpu", "abc123"); got != `gateway-service.process_cpu{span_id="abc123"}` {
		t.Errorf("span-filtered query wrong: %q", got)
	}
}

func TestFlattenFlamebearer(t *testing.T) {
	names, levels := sampleFlame()
	frames := flattenFlamebearer(names, levels, 100)
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames (root + 2 children), got %d: %+v", len(frames), frames)
	}
	// Root spans the whole width.
	root := frames[0]
	if root.Name != "total" || root.Depth != 0 || root.X != 0 || root.Width != 1.0 {
		t.Errorf("root frame wrong: %+v", root)
	}
	// hotFn: depth 1, left-aligned, 60% wide.
	hot := frames[1]
	if hot.Name != "hotFn" || hot.Depth != 1 || hot.X != 0 || hot.Width != 0.6 || hot.Self != 60 {
		t.Errorf("hotFn frame wrong: %+v", hot)
	}
	// coldFn: sits immediately right of hotFn at x=0.6, 40% wide.
	cold := frames[2]
	if cold.Name != "coldFn" || cold.X != 0.6 || cold.Width != 0.4 {
		t.Errorf("coldFn frame wrong (should start at 0.6): %+v", cold)
	}
}

func TestFlattenFlamebearer_ZeroTotalIsSafe(t *testing.T) {
	if got := flattenFlamebearer([]string{"a"}, [][]int{{0, 0, 0, 0}}, 0); len(got) != 0 {
		t.Errorf("zero-total profile should yield no frames, got %+v", got)
	}
	if got := flattenFlamebearer(nil, nil, 0); len(got) != 0 {
		t.Errorf("empty profile should yield no frames, got %+v", got)
	}
}
