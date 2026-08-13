package handler

import "testing"

func TestMessageSimilarity(t *testing.T) {
	// Same words → high similarity (placeholder <num> is ignored).
	s := messageSimilarity(
		"connection timeout after <num> ms to database",
		"connection timeout after <num> ms to cache",
	)
	if s < 0.6 {
		t.Errorf("near-identical messages should score high, got %.2f", s)
	}

	// Disjoint vocabularies → low.
	if s := messageSimilarity("null pointer dereference", "disk quota exceeded"); s > 0.1 {
		t.Errorf("unrelated messages should score low, got %.2f", s)
	}

	// Identical → 1.
	if s := messageSimilarity("payment gateway refused charge", "payment gateway refused charge"); s != 1 {
		t.Errorf("identical messages should be 1.0, got %.2f", s)
	}

	// Two empty → treated identical; empty vs non-empty → disjoint.
	if s := messageSimilarity("", ""); s != 1 {
		t.Errorf("two empty = 1.0, got %.2f", s)
	}
	if s := messageSimilarity("", "something failed"); s != 0 {
		t.Errorf("empty vs non-empty = 0, got %.2f", s)
	}
}

func TestSimilarityTokens_DropsNoiseAndPlaceholders(t *testing.T) {
	toks := similarityTokens("Error <num> at db to be or query")
	// Dropped: the <num> placeholder and sub-3-char words (at/to/be/or/db).
	for _, noise := range []string{"num", "at", "to", "be", "or", "db"} {
		if _, ok := toks[noise]; ok {
			t.Errorf("token %q should have been dropped (short/placeholder): %v", noise, toks)
		}
	}
	if _, ok := toks["error"]; !ok {
		t.Errorf("meaningful token 'error' should be kept: %v", toks)
	}
}
