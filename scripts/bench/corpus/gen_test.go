package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Reproducibility is the whole contract. If the corpus drifts between runs, a
// benchmark cannot distinguish a slower engine from a heavier dataset, and every
// number in BENCHMARK.md becomes unfalsifiable.
func TestGenerate_SameSeedIsByteIdentical(t *testing.T) {
	var a, b bytes.Buffer
	nA, sumA, err := Generate(&a, 42, 2<<20)
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	nB, sumB, err := Generate(&b, 42, 2<<20)
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}
	if nA != nB {
		t.Errorf("same seed produced different sizes: %d vs %d", nA, nB)
	}
	if sumA != sumB {
		t.Errorf("same seed produced different content:\n  %s\n  %s", sumA, sumB)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("same seed produced different bytes despite matching hashes")
	}
}

func TestGenerate_DifferentSeedsDiffer(t *testing.T) {
	var a, b bytes.Buffer
	_, sumA, _ := Generate(&a, 42, 1<<20)
	_, sumB, _ := Generate(&b, 43, 1<<20)
	if sumA == sumB {
		t.Error("different seeds produced identical corpora — the seed is not actually driving generation")
	}
}

// No wall-clock may leak into the payload, or "same seed" would silently stop
// meaning "same bytes" across runs.
func TestGenerate_TimestampsAreDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	Generate(&a, 7, 512<<10)
	Generate(&b, 7, 512<<10)

	firstTS := func(buf *bytes.Buffer) string {
		line, _ := buf.ReadString('\n')
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return r.Timestamp
	}
	if x, y := firstTS(&a), firstTS(&b); x != y {
		t.Errorf("timestamps differ across runs (%s vs %s) — a clock has leaked into generation", x, y)
	}
}

func TestGenerate_SignalMixAndShape(t *testing.T) {
	var buf bytes.Buffer
	if _, _, err := Generate(&buf, 42, 4<<20); err != nil {
		t.Fatalf("generate: %v", err)
	}

	counts := map[string]int{}
	seenCustomers := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("corpus must be valid NDJSON: %v", err)
		}
		counts[r.Signal]++
		if r.Timestamp == "" {
			t.Fatal("every record needs a timestamp; loaders and time-range queries depend on it")
		}
		if c, ok := r.Attrs["customer_id"]; ok {
			seenCustomers[c] = struct{}{}
		}
	}

	for _, sig := range []string{"log", "trace", "metric"} {
		if counts[sig] == 0 {
			t.Errorf("no %q records emitted; the query suite exercises all three signals", sig)
		}
	}
	if counts["log"] <= counts["trace"] {
		t.Errorf("logs should dominate the mix (they dominate real spend): logs=%d traces=%d", counts["log"], counts["trace"])
	}
	// The high-cardinality dimension is the point of the group-by query class;
	// if it collapses, that measurement stops being meaningful.
	if len(seenCustomers) < 100 {
		t.Errorf("expected a high-cardinality customer dimension, saw only %d distinct values", len(seenCustomers))
	}
}
