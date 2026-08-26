package db

import (
	"strings"
	"testing"
)

// String literals must survive translation byte-for-byte.
//
// This is the failure mode with no symptom: every rule in rewrite.go is a regex
// over statement text, so applied to a whole statement they rewrite the *words*
// inside literals too. `VALUES ('the jsonb column')` became
// `VALUES ('the TEXT column')` — valid SQL, applied cleanly, green CI, wrong
// data in the row. Six of seven of these were corrupted before segments.go.
func TestRewriteLeavesStringLiteralsAlone(t *testing.T) {
	for _, stmt := range []string{
		`INSERT INTO notes (body) VALUES ('cast with a::b inside')`,
		`INSERT INTO notes (body) VALUES ('call now() when ready')`,
		`INSERT INTO notes (body) VALUES ('the jsonb column')`,
		`INSERT INTO notes (body) VALUES ('DEFAULT true means yes')`,
		`INSERT INTO notes (body) VALUES ('a timestamp value')`,
		`INSERT INTO notes (body) VALUES ('a uuid and a BIGSERIAL')`,
		`INSERT INTO abac_policies (condition) VALUES ('role == "admin" AND uuid')`,
		// An embedded quote must not end the literal early.
		`INSERT INTO notes (body) VALUES ('it''s a jsonb column')`,
		// Comments are not code either.
		"-- the jsonb column\nSELECT 1",
	} {
		if got := rewriteForSQLite(stmt); got != stmt {
			t.Errorf("literal corrupted\n  in:  %s\n  out: %s", stmt, got)
		}
	}
}

// And the code around a literal must still be translated.
func TestRewriteStillTranslatesCodeBesideLiterals(t *testing.T) {
	in := `ALTER TABLE t ADD COLUMN meta JSONB DEFAULT '{"note":"jsonb"}'`
	got := rewriteForSQLite(in)
	if !strings.Contains(got, "ADD COLUMN meta TEXT") {
		t.Errorf("the JSONB *type* was not translated: %s", got)
	}
	if !strings.Contains(got, `'{"note":"jsonb"}'`) {
		t.Errorf("the literal was altered: %s", got)
	}
}

// The same guarantee for query-time placeholder rewriting, where corruption is
// worse: it shifts every parameter after the literal, so arguments land in the
// wrong columns.
func TestPlaceholderRewriteLeavesLiteralsAlone(t *testing.T) {
	in := `SELECT * FROM t WHERE note = 'costs $5 and $10' AND id = $1 AND b = $2`
	want := `SELECT * FROM t WHERE note = 'costs $5 and $10' AND id = ? AND b = ?`
	if got := RewritePlaceholders(in); got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// The scanner must classify each construct correctly, since everything above
// depends on it.
func TestScanSegmentsClassifies(t *testing.T) {
	segs := scanSegments(`SELECT 'lit', "ident" -- trailing
FROM t`)
	var literals []string
	for _, s := range segs {
		if !s.code {
			literals = append(literals, strings.TrimSpace(s.text))
		}
	}
	want := []string{`'lit'`, `"ident"`, `-- trailing`}
	if len(literals) != len(want) {
		t.Fatalf("found %v, want %v", literals, want)
	}
	for i := range want {
		if literals[i] != want[i] {
			t.Errorf("segment %d = %q, want %q", i, literals[i], want[i])
		}
	}
}

// Reassembly must be lossless regardless of content: whatever the scanner does,
// concatenating its segments has to reproduce the input exactly.
func TestScanSegmentsIsLossless(t *testing.T) {
	for _, in := range []string{
		`SELECT 1`,
		`SELECT 'a''b' FROM "t" -- note
/* block */ WHERE x = $1`,
		`DO $$ BEGIN NULL; END $$`,
		`SELECT 'unterminated`,
		``,
	} {
		var b strings.Builder
		for _, s := range scanSegments(in) {
			b.WriteString(s.text)
		}
		if b.String() != in {
			t.Errorf("lossy\n  in:  %q\n  out: %q", in, b.String())
		}
	}
}
