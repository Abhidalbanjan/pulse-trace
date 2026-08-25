package db

import (
	"regexp"
	"strings"
)

// Translating Postgres DDL into SQLite.
//
// # Scope, and what this is not
//
// This handles the constructs the migrations in this repository actually use.
// It is not a general Postgres-to-SQLite compiler, and pretending otherwise
// would be the more dangerous choice: a translator that silently mistranslates
// something it does not understand produces a schema that looks applied and is
// subtly wrong. Anything unrecognised is left alone, so SQLite rejects it loudly
// at migration time rather than accepting a mangled version.
//
// A migration that genuinely cannot be translated gets a hand-written
// `NNN_name.sqlite.sql` sibling. That is the escape hatch, and it is preferable
// to widening these rules until they are guessing.

var (
	// BIGSERIAL/SERIAL are Postgres sequence types. SQLite's rowid alias gives
	// the same auto-increment behaviour, but only when spelled exactly
	// INTEGER PRIMARY KEY — BIGINT PRIMARY KEY does not alias the rowid and
	// silently stops auto-incrementing.
	reBigSerialPK = regexp.MustCompile(`(?i)\b(big)?serial\b\s+primary\s+key`)
	reBigSerial   = regexp.MustCompile(`(?i)\b(big)?serial\b`)

	// JSONB has no SQLite equivalent; TEXT plus the JSON1 functions is the
	// documented substitute and json_extract works over it.
	reJSONB = regexp.MustCompile(`(?i)\bjsonb\b`)

	// TIMESTAMPTZ / TIMESTAMP WITH TIME ZONE: SQLite has no date type at all.
	// TEXT holding RFC3339 sorts and compares correctly, which is the property
	// the queries rely on.
	reTimestampTZ = regexp.MustCompile(`(?i)\btimestamptz\b|\btimestamp\s+with\s+time\s+zone\b`)
	// No lookahead: Go's regexp is RE2. None is needed — `\b` cannot match
	// inside CURRENT_TIMESTAMP, because `_` is a word character and there is
	// therefore no boundary before `timestamp` there.
	reTimestamp = regexp.MustCompile(`(?i)\btimestamp\b`)

	reNow           = regexp.MustCompile(`(?i)\bnow\s*\(\s*\)`)
	reCurrentTSFunc = regexp.MustCompile(`(?i)\bcurrent_timestamp\s*\(\s*\)`)

	// Postgres-only column and type spellings with direct SQLite equivalents.
	// Likewise safe without lookahead: gen_random_uuid and uuid_generate_v4
	// have a word character before `uuid`, so `\b` does not match there.
	reUUIDType   = regexp.MustCompile(`(?i)\buuid\b`)
	reBoolDefT   = regexp.MustCompile(`(?i)\bdefault\s+true\b`)
	reBoolDefF   = regexp.MustCompile(`(?i)\bdefault\s+false\b`)
	reGenRandom  = regexp.MustCompile(`(?i)\bgen_random_uuid\s*\(\s*\)`)
	reTextArray  = regexp.MustCompile(`(?i)\btext\s*\[\s*\]`)
	reIfNotExist = regexp.MustCompile(`(?i)\bcreate\s+index\s+concurrently\b`)

	// $1, $2 … → ?. Applied only to statements, never to string literals, so
	// the caller must not pass user text through here.
	rePlaceholder = regexp.MustCompile(`\$(\d+)`)
)

// rewriteForSQLite translates one Postgres statement.
func rewriteForSQLite(stmt string) string {
	out := stmt

	// Order matters: the PRIMARY KEY form must be matched before the bare one,
	// or `BIGSERIAL PRIMARY KEY` becomes `BIGINT PRIMARY KEY` and stops
	// auto-incrementing — a failure that appears as duplicate-key errors much
	// later, in whichever table got the most inserts.
	out = reBigSerialPK.ReplaceAllString(out, "INTEGER PRIMARY KEY AUTOINCREMENT")
	out = reBigSerial.ReplaceAllString(out, "INTEGER")

	out = reJSONB.ReplaceAllString(out, "TEXT")
	out = reTimestampTZ.ReplaceAllString(out, "TEXT")
	out = reTimestamp.ReplaceAllString(out, "TEXT")
	out = reGenRandom.ReplaceAllString(out, "(lower(hex(randomblob(16))))")
	out = reUUIDType.ReplaceAllString(out, "TEXT")
	out = reTextArray.ReplaceAllString(out, "TEXT")

	out = reCurrentTSFunc.ReplaceAllString(out, "CURRENT_TIMESTAMP")
	out = reNow.ReplaceAllString(out, "CURRENT_TIMESTAMP")

	out = reBoolDefT.ReplaceAllString(out, "DEFAULT 1")
	out = reBoolDefF.ReplaceAllString(out, "DEFAULT 0")

	// CONCURRENTLY is a Postgres locking refinement with no meaning where there
	// is one writer.
	out = reIfNotExist.ReplaceAllString(out, "CREATE INDEX")

	return out
}

// RewritePlaceholders converts $1-style parameters to ?.
//
// Separate from Rewrite because the two apply at different times: Rewrite runs
// over migration DDL at startup, this runs over query text at call time. Fusing
// them would mean running a dozen DDL regexes over every query the process ever
// executes.
func RewritePlaceholders(query string) string {
	return rePlaceholder.ReplaceAllString(query, "?")
}

// Rebind converts a Postgres-style query for the given dialect, leaving it
// untouched on Postgres.
func Rebind(d Dialect, query string) string {
	if d.Kind() == Postgres {
		return query
	}
	return RewritePlaceholders(query)
}

// splitStatements breaks a migration file into individual statements.
//
// SQLite's driver executes one statement per Exec, while Postgres accepts a
// whole file. Splitting on semicolons outside quotes and dollar-quoted blocks
// is enough for the DDL here; a statement containing a semicolon inside a
// PL/pgSQL body is exactly the case that gets a `.sqlite.sql` sibling instead.
func splitStatements(script string) []string {
	var (
		out                          []string
		cur                          strings.Builder
		inSingle, inDouble, inDollar bool
		dollarTag                    string
	)
	runes := []rune(script)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inDollar:
			cur.WriteRune(c)
			if strings.HasSuffix(cur.String(), dollarTag) {
				inDollar = false
			}
			continue
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '$' && !inSingle && !inDouble:
			if tag := dollarQuoteTag(runes[i:]); tag != "" {
				inDollar, dollarTag = true, tag
				cur.WriteString(tag)
				i += len([]rune(tag)) - 1
				continue
			}
		case c == ';' && !inSingle && !inDouble:
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteRune(c)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// dollarQuoteTag returns the $tag$ opening at the start of r, or "".
func dollarQuoteTag(r []rune) string {
	if len(r) == 0 || r[0] != '$' {
		return ""
	}
	for i := 1; i < len(r) && i < 64; i++ {
		if r[i] == '$' {
			return string(r[:i+1])
		}
		if !isTagRune(r[i]) {
			return ""
		}
	}
	return ""
}

func isTagRune(c rune) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
