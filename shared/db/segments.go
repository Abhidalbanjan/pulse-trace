package db

import "strings"

// Splitting SQL into the parts that may be rewritten and the parts that must not.
//
// # Why this exists
//
// Every rule in rewrite.go is a regular expression over statement text, and SQL
// statements contain string literals. Applied naively, `jsonb` → `TEXT` rewrites
// the *word* jsonb wherever it appears — including inside
// `VALUES ('the jsonb column')`, which becomes `('the TEXT column')`.
//
// That is the worst failure mode available here. It raises no error: the
// statement is valid, the migration applies, CI goes green, and the row contains
// different data than it was written to contain. Measured before this existed,
// six of seven probe statements were corrupted, and `RewritePlaceholders` turned
// `'costs $5 and $10'` into `'costs ? and ?'` — which additionally shifts every
// bind parameter after it.
//
// So rewriting happens per-segment: code is rewritten, everything quoted or
// commented is passed through byte-for-byte.

// segment is a run of statement text. code segments may be rewritten; the rest
// are literals, quoted identifiers or comments and must survive untouched.
type segment struct {
	text string
	code bool
}

// scanSegments splits SQL into alternating code and non-code runs.
//
// Recognises what Postgres and SQLite actually use here: single-quoted strings
// (with ” as the embedded quote), double-quoted identifiers, line and block
// comments, and dollar-quoted bodies. Anything it does not recognise stays code,
// which is the safe default — a missed literal risks corruption, but a
// misidentified literal would silently stop translating real SQL.
func scanSegments(sql string) []segment {
	var (
		out []segment
		cur strings.Builder
		r   = []rune(sql)
	)
	flush := func(code bool) {
		if cur.Len() > 0 {
			out = append(out, segment{text: cur.String(), code: code})
			cur.Reset()
		}
	}

	for i := 0; i < len(r); i++ {
		c := r[i]

		switch {
		case c == '\'':
			flush(true)
			cur.WriteRune(c)
			for i++; i < len(r); i++ {
				cur.WriteRune(r[i])
				if r[i] == '\'' {
					// '' is an escaped quote, not the end of the literal.
					if i+1 < len(r) && r[i+1] == '\'' {
						i++
						cur.WriteRune(r[i])
						continue
					}
					break
				}
			}
			flush(false)

		case c == '"':
			flush(true)
			cur.WriteRune(c)
			for i++; i < len(r); i++ {
				cur.WriteRune(r[i])
				if r[i] == '"' {
					break
				}
			}
			flush(false)

		case c == '-' && i+1 < len(r) && r[i+1] == '-':
			flush(true)
			for ; i < len(r); i++ {
				cur.WriteRune(r[i])
				if r[i] == '\n' {
					break
				}
			}
			flush(false)

		case c == '/' && i+1 < len(r) && r[i+1] == '*':
			flush(true)
			cur.WriteRune(r[i])
			for i++; i < len(r); i++ {
				cur.WriteRune(r[i])
				if r[i] == '/' && r[i-1] == '*' {
					break
				}
			}
			flush(false)

		case c == '$':
			if tag := dollarQuoteTag(r[i:]); tag != "" {
				flush(true)
				cur.WriteString(tag)
				i += len([]rune(tag)) - 1
				for i++; i < len(r); i++ {
					cur.WriteRune(r[i])
					if strings.HasSuffix(cur.String(), tag) {
						break
					}
				}
				flush(false)
				continue
			}
			cur.WriteRune(c)

		default:
			cur.WriteRune(c)
		}
	}
	flush(true)
	return out
}

// mapCode applies f to the code parts of sql, leaving literals and comments
// exactly as they were.
func mapCode(sql string, f func(string) string) string {
	var b strings.Builder
	for _, seg := range scanSegments(sql) {
		if seg.code {
			b.WriteString(f(seg.text))
		} else {
			b.WriteString(seg.text)
		}
	}
	return b.String()
}

// codeMask returns sql with every non-code rune replaced by a space.
//
// Same length as the input, so an offset found in the mask indexes the original
// exactly. This is how a rule that must see a whole *statement* — rather than
// one code run — can still avoid matching inside a literal: match on the mask,
// splice into the original.
func codeMask(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	for _, seg := range scanSegments(sql) {
		if seg.code {
			b.WriteString(seg.text)
			continue
		}
		for range []rune(seg.text) {
			b.WriteByte(' ')
		}
	}
	return b.String()
}
