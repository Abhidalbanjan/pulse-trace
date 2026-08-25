package db

import (
	"context"
	"database/sql"
	"fmt"
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

	// Postgres cast syntax. `'["*"]'::jsonb` is a no-op once jsonb is TEXT, and
	// SQLite has no `::` at all — it reports `unrecognized token: ":"`, which
	// points at the colon rather than the cast and is why this took a probe
	// rather than a reading to find.
	reCast = regexp.MustCompile(`::\s*[A-Za-z_][A-Za-z0-9_]*(\s*\[\s*\])?`)

	// SQLite has no `IF NOT EXISTS` on ADD COLUMN. Stripping it changes the
	// statement's meaning — it stops being idempotent — so execStatement
	// restores that by tolerating the duplicate-column error, and only for
	// statements that carried the clause in the first place.
	reAddColumnIfNotExists = regexp.MustCompile(`(?i)\badd\s+column\s+if\s+not\s+exists\b`)

	// `INSERT … SELECT … ON CONFLICT` needs an intervening WHERE in SQLite,
	// which otherwise parses `ON` as the start of a join clause. Documented
	// quirk with a documented workaround.
	reInsertSelectUpsert = regexp.MustCompile(`(?is)(INSERT\s+INTO\b.*?\bSELECT\b.*?)(\s+ON\s+CONFLICT\b)`)
	reHasWhere           = regexp.MustCompile(`(?i)\bwhere\b`)

	// NULLS FIRST/LAST is accepted by SQLite in ORDER BY but not in an index
	// definition. Stripping it is only safe in the index case — in an ORDER BY
	// it decides where NULLs sort, and removing it would silently reorder
	// results. Hence a statement-aware rule rather than a global one.
	reCreateIndex = regexp.MustCompile(`(?is)^\s*CREATE\s+(UNIQUE\s+)?INDEX\b`)
	reNullsOrder  = regexp.MustCompile(`(?i)\s+NULLS\s+(FIRST|LAST)\b`)
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

	out = reCast.ReplaceAllString(out, "")
	// Statement-aware: see reNullsOrder.
	if _, body := splitLeadingComments(out); reCreateIndex.MatchString(body) {
		out = reNullsOrder.ReplaceAllString(out, "")
	}
	out = reAddColumnIfNotExists.ReplaceAllString(out, "ADD COLUMN")
	out = fixInsertSelectUpsert(out)

	return out
}

// fixInsertSelectUpsert inserts the WHERE that SQLite's parser requires between
// a SELECT source and an ON CONFLICT clause.
//
// Only when the SELECT does not already have one: adding a second WHERE would
// be a syntax error, and adding `WHERE true` to a filtered SELECT would change
// which rows it inserts.
func fixInsertSelectUpsert(stmt string) string {
	return reInsertSelectUpsert.ReplaceAllStringFunc(stmt, func(m string) string {
		loc := reInsertSelectUpsert.FindStringSubmatchIndex(m)
		if loc == nil {
			return m
		}
		body, upsert := m[loc[2]:loc[3]], m[loc[4]:loc[5]]
		if reHasWhere.MatchString(body) {
			return m
		}
		return body + " WHERE true" + upsert
	})
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
		out       []string
		cur       strings.Builder
		inSingle  bool
		inDouble  bool
		inDollar  bool
		inLine    bool
		inBlock   bool
		dollarTag string
	)
	r := []rune(script)
	for i := 0; i < len(r); i++ {
		c := r[i]

		// Comments first. A `;` or `:` inside one is not syntax, and treating it
		// as a terminator splits a statement mid-sentence — which is what the
		// first version of this did to 19 of 26 migrations, producing errors
		// like `near "this": syntax error` from prose.
		switch {
		case inLine:
			cur.WriteRune(c)
			if c == '\n' {
				inLine = false
			}
			continue
		case inBlock:
			cur.WriteRune(c)
			if c == '/' && i > 0 && r[i-1] == '*' {
				inBlock = false
			}
			continue
		case inDollar:
			cur.WriteRune(c)
			if strings.HasSuffix(cur.String(), dollarTag) {
				inDollar = false
			}
			continue
		}

		if !inSingle && !inDouble {
			if c == '-' && i+1 < len(r) && r[i+1] == '-' {
				inLine = true
				cur.WriteRune(c)
				continue
			}
			if c == '/' && i+1 < len(r) && r[i+1] == '*' {
				inBlock = true
				cur.WriteRune(c)
				continue
			}
		}

		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '$' && !inSingle && !inDouble:
			if tag := dollarQuoteTag(r[i:]); tag != "" {
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

// ExecStatement runs one migration statement, restoring the idempotency the
// rewrite had to strip.
//
// `ADD COLUMN IF NOT EXISTS` means "add it, ignore it if present", and SQLite
// cannot express the second half. Rewriting it to a plain ADD COLUMN keeps the
// first half and loses the second, so a re-run of an applied migration would
// fail on a column that is already there. Tolerating precisely that error, and
// only for statements that carried the clause, puts the meaning back without
// swallowing anything else.
func ExecStatement(ctx context.Context, db execer, d Dialect, original string) error {
	stmt := d.Rewrite(original)
	_, err := db.ExecContext(ctx, stmt)
	if err == nil {
		return nil
	}
	if d.Kind() == SQLite &&
		reAddColumnIfNotExists.MatchString(original) &&
		strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return nil
	}
	return err
}

// execer is satisfied by *sql.DB and *sql.Tx alike.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// SplitStatements exposes the statement splitter to the migration runner.
func SplitStatements(script string) []string { return splitStatements(script) }

// reAlterAddColumns matches an ALTER TABLE that adds several columns at once.
var (
	reAlterAddColumns = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+(\S+)\s+(ADD\s+COLUMN\b.*)$`)
	reAddColumnSplit  = regexp.MustCompile(`(?is),\s*ADD\s+COLUMN\b`)
)

// expandStatement turns one Postgres statement into the one-or-more SQLite
// needs.
//
// Postgres accepts `ALTER TABLE t ADD COLUMN a …, ADD COLUMN b …`; SQLite
// permits exactly one column per ALTER. That is a difference in statement
// *count*, not in text, so no amount of string rewriting expresses it — which
// is why this returns a slice and Rewrite does not.
func expandStatement(stmt string) []string {
	// Statements arrive with their leading comment block attached, so the
	// pattern cannot simply anchor at the start of the string — an earlier
	// version did, matched nothing, and left every multi-column ALTER failing
	// with `near ",": syntax error`.
	lead, body0 := splitLeadingComments(stmt)
	m := reAlterAddColumns.FindStringSubmatch(body0)
	if m == nil {
		return []string{stmt}
	}
	_ = lead // the comments are dropped: they document the original, not each split
	table, body := m[1], m[2]
	parts := reAddColumnSplit.Split(body, -1)
	if len(parts) <= 1 {
		return []string{stmt}
	}
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p), ";"))
		if p == "" {
			continue
		}
		if i > 0 {
			// Only the first part kept its ADD COLUMN; the split consumed the
			// rest along with the comma.
			p = "ADD COLUMN " + p
		}
		out = append(out, fmt.Sprintf("ALTER TABLE %s %s", table, p))
	}
	return out
}

// ExpandStatements splits a script and expands any statement whose Postgres
// form is several SQLite statements.
func ExpandStatements(script string) []string {
	var out []string
	for _, s := range splitStatements(script) {
		out = append(out, expandStatement(s)...)
	}
	return out
}

// splitLeadingComments separates a statement's leading comment lines from its
// SQL, so a pattern anchored at the start of the SQL still matches.
func splitLeadingComments(stmt string) (lead, body string) {
	lines := strings.Split(stmt, "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		return strings.Join(lines[:i], "\n"), strings.Join(lines[i:], "\n")
	}
	return stmt, ""
}
