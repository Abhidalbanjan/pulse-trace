package handler

// Similar-error clustering (Error Tracking · E6).
//
// The same underlying bug often fingerprints into several "issues" (a timeout in
// three call sites, an error phrased two ways). Surfacing the most similar other
// groups lets an operator resolve them together instead of chasing duplicates.
// Similarity is a pure token-Jaccard over the normalized messages so it's fast
// and unit-tested without a database.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// similarityTokens lowercases a normalized error message and splits it into a
// set of meaningful tokens: alphanumeric words of length ≥ 3, with the
// placeholder tokens the fingerprinter injects (<num>, <uuid>, <str>) dropped so
// they don't inflate every pair's similarity. Pure.
func similarityTokens(msg string) map[string]struct{} {
	set := map[string]struct{}{}
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		tok := b.String()
		b.Reset()
		if len(tok) < 3 {
			return
		}
		switch tok {
		case "num", "uuid", "str": // fingerprint placeholders — not distinguishing
			return
		}
		set[tok] = struct{}{}
	}
	for _, r := range strings.ToLower(msg) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return set
}

// messageSimilarity is the Jaccard index (|A∩B| / |A∪B|) of two messages' token
// sets: 1.0 identical, 0.0 disjoint. Two empty messages are treated as identical
// (both carry no signal), an empty vs non-empty as disjoint. Pure.
func messageSimilarity(a, b string) float64 {
	ta, tb := similarityTokens(a), similarityTokens(b)
	if len(ta) == 0 && len(tb) == 0 {
		return 1
	}
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for tok := range ta {
		if _, ok := tb[tok]; ok {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

const similarMinScore = 0.3 // below this, groups aren't meaningfully related

type similarGroup struct {
	Fingerprint string  `json:"fingerprint"`
	Service     string  `json:"service"`
	Operation   string  `json:"operation"`
	Message     string  `json:"message"`
	Sample      string  `json:"sample_message"`
	Similarity  float64 `json:"similarity"`
}

// GetSimilarErrorGroups ranks other recent error groups by message similarity to
// the target group. The target is identified by its service/operation/message
// (like the timeline endpoint); the path fingerprint must match that identity so
// a fabricated fingerprint can't be paired with a mismatched group.
//
//	GET /api/v1/errors/groups/{fingerprint}/similar?service=&operation=&message=&limit=
func (h *ErrorTrackingHandler) GetSimilarErrorGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	fp := r.PathValue("fingerprint")
	service := r.URL.Query().Get("service")
	operation := r.URL.Query().Get("operation")
	message := r.URL.Query().Get("message")
	if service == "" || operation == "" {
		http.Error(w, "service and operation are required", http.StatusBadRequest)
		return
	}
	tenant := tenantFromRequest(r)
	if fp != fingerprint(tenant, service, operation, message) {
		http.Error(w, "fingerprint does not match the supplied group identity", http.StatusBadRequest)
		return
	}

	limit := 5
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 25 {
		limit = l
	}

	observations, err := h.recentErrorGroups(tenant)
	if err != nil {
		// Similarity is advisory; degrade to an empty list rather than erroring.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []similarGroup{}})
		return
	}

	out := make([]similarGroup, 0, len(observations))
	for _, o := range observations {
		if o.service == service && o.operation == operation && o.message == message {
			continue // the group itself
		}
		score := messageSimilarity(message, o.message)
		if score < similarMinScore {
			continue
		}
		out = append(out, similarGroup{
			Fingerprint: fingerprint(tenant, o.service, o.operation, o.message),
			Service:     o.service,
			Operation:   o.operation,
			Message:     o.message,
			Sample:      o.sample,
			Similarity:  score,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	if len(out) > limit {
		out = out[:limit]
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}
