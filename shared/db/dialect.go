// Package db is the storage-dialect layer (P1.3).
//
// # Which drivers this package links
//
// SQLite is not imported here. Blank-importing it linked a translated C
// library — and not a small one — into all eight service binaries to be used by
// one, which is the wrong direction for D1, the very dimension this phase
// exists to improve. A binary that wants it imports
// `db/driver/sqlite` and pays for it deliberately.
//
// Postgres is a different case and the distinction is worth stating rather than
// glossing: `postgres.go` in this same package already blank-imports pgx for
// NewPostgresPool, so anything importing `shared/db` registers the pgx driver
// whether it wants to or not. `db/driver/postgres` exists for symmetry and for
// the day that import moves; today it is a no-op, and claiming this package
// imports neither driver would be false.
//
// # Why this exists
//
// The cluster deployment runs Postgres. A single-binary deployment cannot, and
// requiring it would defeat the point — Postgres is one of the twenty-three
// containers D1 counts. So the same schema and the same code have to run on
// SQLite, which means one place that knows how the two differ.
//
// # The rule this package follows
//
// Postgres is the source of truth and SQLite adapts to it. Never the reverse.
// Degrading the Postgres path to something SQLite can also do would trade the
// deployment most customers run for the one that is easier to support, and the
// differences worth having — JSONB, advisory locks, real concurrency — are
// exactly the ones a lite deployment can live without and a cluster cannot.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
)

// Kind names a supported backend.
type Kind string

const (
	Postgres Kind = "postgres"
	SQLite   Kind = "sqlite"
)

// Dialect is the set of things the two backends do differently.
//
// Deliberately small. Every method here is a place the schema or the runtime
// genuinely diverges; anything that can be written once in portable SQL is not
// on this interface, because each method is a second implementation somebody
// has to keep correct.
type Dialect interface {
	Kind() Kind

	// Placeholder renders the nth bind parameter, 1-based.
	Placeholder(n int) string

	// Rewrite translates Postgres DDL/DML into this dialect.
	Rewrite(stmt string) string

	// LockChain serialises writers of a named append-only chain within tx, so
	// the tail one writer reads cannot be superseded before it writes.
	//
	// This is the method the whole package exists for. See chainlock.go.
	LockChain(ctx context.Context, tx *sql.Tx, name string) error

	// EnsureChainLock creates whatever LockChain needs, for the audit chain and
	// any additional chains named. Called once at startup.
	EnsureChainLock(ctx context.Context, db *sql.DB, chains ...string) error
}

// Open connects by URL scheme and returns the matching dialect.
//
// The scheme is the whole configuration surface: `postgres://…` is a cluster,
// `sqlite:///var/lib/pulsetrace/pulsetrace.db` or a bare path is lite. A
// deployment cannot end up with a Postgres URL and SQLite behaviour, because
// there is no second switch to disagree with the first.
func Open(ctx context.Context, dsn string) (*sql.DB, Dialect, error) {
	kind, driverDSN, err := parseDSN(dsn)
	if err != nil {
		return nil, nil, err
	}

	driver := "pgx"
	if kind == SQLite {
		driver = "sqlite"
	}
	conn, err := sql.Open(driver, driverDSN)
	if err != nil {
		// The commonest cause is a binary that did not import the driver
		// package for the backend its DSN names, so say so rather than
		// surfacing database/sql's bare "unknown driver".
		return nil, nil, fmt.Errorf("db: open %s: %w (does this binary import "+
			"github.com/pulsetrace/shared/db/driver/%s?)", kind, err, kind)
	}
	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("db: ping %s: %w", kind, err)
	}

	var d Dialect
	switch kind {
	case Postgres:
		d = postgresDialect{}
	case SQLite:
		// One writer at a time is not a tuning choice on SQLite, it is the
		// engine's model. Allowing the pool to open several write connections
		// converts that into SQLITE_BUSY errors under exactly the concurrency
		// the chain lock exists to handle.
		conn.SetMaxOpenConns(1)
		d = sqliteDialect{}
	}
	if err := d.EnsureChainLock(ctx, conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, d, nil
}

// parseDSN maps a URL to a backend and the DSN its driver wants.
func parseDSN(dsn string) (Kind, string, error) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return "", "", fmt.Errorf("db: empty DSN")
	}

	switch {
	case strings.HasPrefix(trimmed, "postgres://"), strings.HasPrefix(trimmed, "postgresql://"):
		return Postgres, trimmed, nil

	case strings.HasPrefix(trimmed, "sqlite://"), strings.HasPrefix(trimmed, "file:"):
		path := strings.TrimPrefix(strings.TrimPrefix(trimmed, "sqlite://"), "file:")
		return SQLite, sqliteDSN(path), nil

	case !strings.Contains(trimmed, "://"):
		// A bare path is SQLite. Accepted because `--data-dir` deployments name
		// a file, not a URL, and forcing a scheme on them buys nothing.
		return SQLite, sqliteDSN(trimmed), nil
	}

	u, _ := url.Parse(trimmed)
	scheme := trimmed
	if u != nil {
		scheme = u.Scheme
	}
	return "", "", fmt.Errorf("db: unsupported DSN scheme %q (expected postgres:// or sqlite://)", scheme)
}

// sqliteDSN adds the pragmas the chain lock and the schema depend on.
//
// `busy_timeout` is the one that matters: without it a second writer arriving
// during a held lock gets SQLITE_BUSY immediately rather than waiting, which
// turns the serialisation this package provides into an error the caller has to
// retry. `foreign_keys` because the migrations declare them and SQLite ignores
// them by default — a schema whose constraints are silently not enforced is
// worse than one that never had them.
func sqliteDSN(path string) string {
	const pragmas = "_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	// Merged, not skipped. Returning the path unchanged when it already carries
	// a query string drops all three pragmas — including busy_timeout, which
	// this function's own comment calls the one that matters, and journal_mode,
	// which the chain lock's behaviour depends on. An operator adding
	// `?_pragma=cache_size(2000)` would have silently disabled them.
	if strings.Contains(path, "?") {
		return path + "&" + pragmas
	}
	return path + "?" + pragmas
}
