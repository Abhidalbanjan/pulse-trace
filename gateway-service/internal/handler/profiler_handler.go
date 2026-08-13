package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ProfilerHandler turns raw Pyroscope profiles into a PulseTrace product surface:
// a ranked flat-profile ("top functions") and, crucially, a profile *diff* with
// regression detection computed here rather than delegated to Pyroscope's own
// embedded UI. It reads Pyroscope's render API (flamebearer JSON) server-side and
// returns structured data the frontend renders natively.
type ProfilerHandler struct {
	pyroscopeURL string
	client       *http.Client
}

func NewProfilerHandler(pyroscopeURL string) *ProfilerHandler {
	return &ProfilerHandler{pyroscopeURL: pyroscopeURL, client: &http.Client{Timeout: 10 * time.Second}}
}

// flamebearer is the subset of Pyroscope's render response we need. `levels`
// encodes the flame graph as flat quads per level: (x-offset-delta, total, self,
// nameIndex). Summing `self` per nameIndex yields each function's flat self-time.
type flamebearer struct {
	Flamebearer struct {
		Names    []string `json:"names"`
		Levels   [][]int  `json:"levels"`
		NumTicks int      `json:"numTicks"`
	} `json:"flamebearer"`
}

// aggregateSelf sums self-time per function across every flame-graph node. It's
// pure so the parsing contract is unit-tested without a live Pyroscope.
func aggregateSelf(names []string, levels [][]int) map[string]int64 {
	m := map[string]int64{}
	for _, level := range levels {
		for i := 0; i+3 < len(level); i += 4 {
			self := int64(level[i+2])
			idx := level[i+3]
			if idx >= 0 && idx < len(names) {
				m[names[idx]] += self
			}
		}
	}
	return m
}

// sumSelf totals all self-time (equals numTicks for a valid single profile; used
// as a fallback when numTicks is absent).
func sumSelf(self map[string]int64) int64 {
	var total int64
	for _, v := range self {
		total += v
	}
	return total
}

type funcStat struct {
	Name string  `json:"name"`
	Self int64   `json:"self"`
	Pct  float64 `json:"pct"`
}

// FlameFrame is one positioned rectangle of the flame graph: its depth (row from
// the root), normalized horizontal position/width in [0,1] (fraction of the root
// total, so the FE renders it without knowing sample counts), and its raw
// self/total samples for the hover tooltip.
type FlameFrame struct {
	Depth int     `json:"depth"`
	X     float64 `json:"x"`
	Width float64 `json:"width"`
	Self  int64   `json:"self"`
	Total int64   `json:"total"`
	Name  string  `json:"name"`
}

// flattenFlamebearer decodes Pyroscope's flamebearer level encoding into absolute,
// normalized frames the frontend can lay out directly. Each level is a flat array
// of 4-int bars [xOffsetDelta, total, self, nameIndex]; the x offset is a delta
// from the right edge of the previous bar on the same row, so we accumulate a
// running cursor per level. Widths/positions are normalized by the root total so
// the whole graph spans [0,1]. Pure — the decode contract is unit-tested without
// a live Pyroscope. Malformed bars are skipped rather than throwing.
func flattenFlamebearer(names []string, levels [][]int, total int64) []FlameFrame {
	denom := total
	if len(levels) > 0 && len(levels[0]) >= 2 && levels[0][1] > 0 {
		denom = int64(levels[0][1]) // the root bar's total is the true 100% width
	}
	if denom <= 0 {
		return []FlameFrame{}
	}

	frames := make([]FlameFrame, 0, len(levels)*4)
	for depth, level := range levels {
		cursor := 0 // running left edge (in samples) within this row
		for i := 0; i+3 < len(level); i += 4 {
			offset := level[i]
			barTotal := level[i+1]
			self := level[i+2]
			idx := level[i+3]
			x := cursor + offset
			cursor = x + barTotal
			name := ""
			if idx >= 0 && idx < len(names) {
				name = names[idx]
			}
			frames = append(frames, FlameFrame{
				Depth: depth,
				X:     float64(x) / float64(denom),
				Width: float64(barTotal) / float64(denom),
				Self:  int64(self),
				Total: int64(barTotal),
				Name:  name,
			})
		}
	}
	return frames
}

// topFunctions ranks functions by flat self-time (descending), skipping the
// synthetic root, and returns the top `limit` with each one's share of total.
func topFunctions(self map[string]int64, total int64, limit int) []funcStat {
	out := make([]funcStat, 0, len(self))
	for name, s := range self {
		if name == "total" || name == "" || s == 0 {
			continue
		}
		pct := 0.0
		if total > 0 {
			pct = float64(s) / float64(total) * 100
		}
		out = append(out, funcStat{Name: name, Self: s, Pct: pct})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Self != out[j].Self {
			return out[i].Self > out[j].Self
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

type funcDiff struct {
	Name           string  `json:"name"`
	BaselineSelf   int64   `json:"baseline_self"`
	ComparisonSelf int64   `json:"comparison_self"`
	BaselinePct    float64 `json:"baseline_pct"`
	ComparisonPct  float64 `json:"comparison_pct"`
	DeltaPct       float64 `json:"delta_pct"` // percentage-POINT change (comparison − baseline share)
	Regression     bool    `json:"regression"`
}

// diffProfiles compares two flat profiles by each function's SHARE of total
// self-time (percentage points), not raw samples — so a uniform load increase
// doesn't read as every function regressing. A function whose share grew by at
// least regressionThresholdPP is flagged as a regression. Sorted by the largest
// share increase first (the things that got worse), then the improvements.
func diffProfiles(base, comp map[string]int64, baseTotal, compTotal int64, regressionThresholdPP float64, limit int) []funcDiff {
	names := map[string]struct{}{}
	for n := range base {
		names[n] = struct{}{}
	}
	for n := range comp {
		names[n] = struct{}{}
	}

	pct := func(v, total int64) float64 {
		if total <= 0 {
			return 0
		}
		return float64(v) / float64(total) * 100
	}

	out := make([]funcDiff, 0, len(names))
	for name := range names {
		if name == "total" || name == "" {
			continue
		}
		b, c := base[name], comp[name]
		if b == 0 && c == 0 {
			continue
		}
		bp, cp := pct(b, baseTotal), pct(c, compTotal)
		delta := cp - bp
		out = append(out, funcDiff{
			Name: name, BaselineSelf: b, ComparisonSelf: c,
			BaselinePct: bp, ComparisonPct: cp, DeltaPct: delta,
			Regression: delta >= regressionThresholdPP,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeltaPct != out[j].DeltaPct {
			return out[i].DeltaPct > out[j].DeltaPct
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

const (
	profilerRegressionThresholdPP = 1.0 // a +1 share-point shift is a real regression
	profilerTopN                  = 40
)

// buildQuery assembles a Pyroscope app query: <service>.<profileType>{selector}.
// span_id narrows to samples pprof-labelled with that span (trace↔profile link).
func buildProfilerQuery(service, profileType, spanID string) string {
	selector := "{}"
	if spanID != "" {
		selector = fmt.Sprintf(`{span_id="%s"}`, spanID)
	}
	return service + "." + profileType + selector
}

// rangeSeconds parses the window, defaulting/capping to a sane range.
func profilerRange(r *http.Request) int64 {
	sec, _ := strconv.ParseInt(r.URL.Query().Get("range_seconds"), 10, 64)
	if sec <= 0 {
		sec = 3600
	}
	if sec > 24*3600 {
		sec = 24 * 3600
	}
	return sec
}

// fetchProfile pulls one window's flat self-time map from Pyroscope's render API.
// A backend/parse failure degrades to an empty profile (nil map) so the surface
// shows an honest empty state rather than erroring — profiling is advisory.
func (h *ProfilerHandler) fetchProfile(query string, from, until int64) (map[string]int64, int64) {
	endpoint := fmt.Sprintf("%s/render?query=%s&from=%d&until=%d&format=json&maxNodes=2048",
		strings.TrimRight(h.pyroscopeURL, "/"), url.QueryEscape(query), from, until)

	resp, err := h.client.Get(endpoint)
	if err != nil {
		log.Printf("[ProfilerHandler] render call failed for %q: %v", query, err)
		return nil, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[ProfilerHandler] render returned %d for %q", resp.StatusCode, query)
		return nil, 0
	}
	var fb flamebearer
	if err := json.NewDecoder(resp.Body).Decode(&fb); err != nil {
		log.Printf("[ProfilerHandler] failed to decode flamebearer for %q: %v", query, err)
		return nil, 0
	}
	self := aggregateSelf(fb.Flamebearer.Names, fb.Flamebearer.Levels)
	total := int64(fb.Flamebearer.NumTicks)
	if total == 0 {
		total = sumSelf(self)
	}
	return self, total
}

// fetchFlame is like fetchProfile but also returns the raw flamebearer tree
// (names + levels) so callers can render an interactive flame graph, not just the
// flattened top-functions list. Same advisory failure semantics (empty on error).
func (h *ProfilerHandler) fetchFlame(query string, from, until int64) (names []string, levels [][]int, total int64) {
	endpoint := fmt.Sprintf("%s/render?query=%s&from=%d&until=%d&format=json&maxNodes=2048",
		strings.TrimRight(h.pyroscopeURL, "/"), url.QueryEscape(query), from, until)

	resp, err := h.client.Get(endpoint)
	if err != nil {
		log.Printf("[ProfilerHandler] render call failed for %q: %v", query, err)
		return nil, nil, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[ProfilerHandler] render returned %d for %q", resp.StatusCode, query)
		return nil, nil, 0
	}
	var fb flamebearer
	if err := json.NewDecoder(resp.Body).Decode(&fb); err != nil {
		log.Printf("[ProfilerHandler] failed to decode flamebearer for %q: %v", query, err)
		return nil, nil, 0
	}
	total = int64(fb.Flamebearer.NumTicks)
	if total == 0 {
		total = sumSelf(aggregateSelf(fb.Flamebearer.Names, fb.Flamebearer.Levels))
	}
	return fb.Flamebearer.Names, fb.Flamebearer.Levels, total
}

// GetFunctions returns the ranked flat profile (top functions by self-time) for a
// service over the requested window — the native alternative to embedding
// Pyroscope's own flame-graph UI in an iframe.
//
//	GET /api/v1/profiler/functions?service=&profile_type=&range_seconds=&span_id=
func (h *ProfilerHandler) GetFunctions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	service := r.URL.Query().Get("service")
	profileType := r.URL.Query().Get("profile_type")
	if service == "" || profileType == "" {
		http.Error(w, "service and profile_type are required", http.StatusBadRequest)
		return
	}
	sec := profilerRange(r)
	until := time.Now().Unix()
	from := until - sec

	names, levels, total := h.fetchFlame(buildProfilerQuery(service, profileType, r.URL.Query().Get("span_id")), from, until)
	self := aggregateSelf(names, levels)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"service":      service,
		"profile_type": profileType,
		"total":        total,
		"functions":    topFunctions(self, total, profilerTopN),
		// flame carries the full positioned tree (Profiler · E1) so the FE can
		// render an interactive flame graph; the flat "functions" list stays for
		// the existing table. Backward-compatible additive field.
		"flame": flattenFlamebearer(names, levels, total),
	})
}

// GetDiff compares the current window against the immediately preceding window of
// the same length and returns a per-function share diff plus the regressions — a
// profile regression view surfaced by PulseTrace, not Pyroscope's embedded diff.
//
//	GET /api/v1/profiler/diff?service=&profile_type=&range_seconds=&span_id=
func (h *ProfilerHandler) GetDiff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	service := r.URL.Query().Get("service")
	profileType := r.URL.Query().Get("profile_type")
	if service == "" || profileType == "" {
		http.Error(w, "service and profile_type are required", http.StatusBadRequest)
		return
	}
	sec := profilerRange(r)
	now := time.Now().Unix()
	compFrom, compUntil := now-sec, now
	baseFrom, baseUntil := now-2*sec, now-sec

	query := buildProfilerQuery(service, profileType, r.URL.Query().Get("span_id"))
	baseSelf, baseTotal := h.fetchProfile(query, baseFrom, baseUntil)
	compSelf, compTotal := h.fetchProfile(query, compFrom, compUntil)

	diffs := diffProfiles(baseSelf, compSelf, baseTotal, compTotal, profilerRegressionThresholdPP, profilerTopN)
	regressions := make([]funcDiff, 0)
	for _, d := range diffs {
		if d.Regression {
			regressions = append(regressions, d)
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"service":          service,
		"profile_type":     profileType,
		"baseline_total":   baseTotal,
		"comparison_total": compTotal,
		"regression_count": len(regressions),
		"functions":        diffs,
		"regressions":      regressions,
	})
}
