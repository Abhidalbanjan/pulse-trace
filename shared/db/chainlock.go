package db

import (
	"context"
	"database/sql"
	"fmt"
	"hash/crc32"
)

// The chain lock.
//
// # What it protects, and why an ordinary transaction is not enough
//
// The audit log is hash-chained: each row's entry_hash is computed from the
// previous row's hash plus this row's content, so altering any persisted row
// breaks the chain and is detectable by replaying it (F20).
//
// That property depends on one thing — that the tail a writer reads is still
// the tail when it writes. Two writers that both read tail H and both append
// with prev_hash = H produce a *fork*: two rows claiming the same predecessor.
// Verification then passes for either branch taken alone, so the chain still
// looks intact while no longer being a single ordered history. Tamper-evidence
// is gone and nothing reports it.
//
// A transaction alone does not prevent this. Under Postgres' default READ
// COMMITTED both transactions read the tail before either commits, and neither
// conflicts with the other: they insert different rows. There is no write-write
// conflict for the database to detect, because the constraint being violated is
// semantic and lives in the application.
//
// So writers must be serialised explicitly, which is what this is.
//
// # Why the two implementations look nothing alike
//
// Postgres has advisory locks: a transaction-scoped lock on an arbitrary key,
// released automatically at commit or rollback, with no table involved.
//
// SQLite has no such thing. What it has is a single-writer model: any statement
// that writes takes a RESERVED lock on the database and holds it until commit.
// Taking that lock deliberately — by updating a row that exists only to be
// updated — gives the same guarantee for the same duration. It works *because*
// SQLite's concurrency is limited, which is an unusual case of a weakness being
// exactly the right primitive.

// auditChainName is the chain the audit log uses. Named rather than numeric so
// a second chain (P9's remediation ledger, say) is a new string and not a magic
// integer somebody has to check for collisions.
const auditChainName = "audit_log"

// ── Postgres ─────────────────────────────────────────────────────────────────

type postgresDialect struct{}

func (postgresDialect) Kind() Kind { return Postgres }

func (postgresDialect) Placeholder(n int) string { return fmt.Sprintf("$%d", n) }

func (postgresDialect) Rewrite(stmt string) string { return stmt }

// EnsureChainLock is a no-op: advisory locks need no storage.
func (postgresDialect) EnsureChainLock(context.Context, *sql.DB) error { return nil }

func (postgresDialect) LockChain(ctx context.Context, tx *sql.Tx, name string) error {
	// The lock is transaction-scoped, so it is released by commit or rollback
	// without an unlock call that could be skipped on an error path.
	_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", chainLockKey(name))
	if err != nil {
		return fmt.Errorf("db: acquire advisory lock for chain %q: %w", name, err)
	}
	return nil
}

// chainLockKey maps a chain name to the int64 advisory-lock namespace.
//
// A collision would mean two unrelated chains serialising against each other:
// slower, never incorrect. Correctness does not depend on this being injective,
// which is why a checksum is adequate and a registry of hand-assigned integers
// is not worth maintaining.
func chainLockKey(name string) int64 {
	return int64(crc32.ChecksumIEEE([]byte(name)))
}

// ── SQLite ───────────────────────────────────────────────────────────────────

type sqliteDialect struct{}

func (sqliteDialect) Kind() Kind { return SQLite }

func (sqliteDialect) Placeholder(int) string { return "?" }

func (sqliteDialect) Rewrite(stmt string) string { return rewriteForSQLite(stmt) }

// EnsureChainLock creates the table whose rows are the locks.
func (sqliteDialect) EnsureChainLock(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS chain_lock (
			name TEXT PRIMARY KEY,
			holder_seq INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		return fmt.Errorf("db: create chain_lock: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO chain_lock (name, holder_seq) VALUES (?, 0) ON CONFLICT (name) DO NOTHING`,
		auditChainName); err != nil {
		return fmt.Errorf("db: seed chain_lock: %w", err)
	}
	return nil
}

func (sqliteDialect) LockChain(ctx context.Context, tx *sql.Tx, name string) error {
	// An UPDATE, not a SELECT. A read takes only a SHARED lock, which several
	// transactions hold at once — precisely the situation that forks the chain.
	// Writing the row takes RESERVED, which exactly one transaction can hold,
	// and holds it until commit.
	//
	// The row must already exist; INSERT-on-missing here would let two writers
	// both insert and both proceed. EnsureChainLock seeds it at startup.
	res, err := tx.ExecContext(ctx,
		`UPDATE chain_lock SET holder_seq = holder_seq + 1 WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("db: acquire chain lock %q: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: acquire chain lock %q: %w", name, err)
	}
	if n == 0 {
		// Failing closed. Continuing without the lock would produce a chain that
		// verifies today and forks under the first concurrent write.
		return fmt.Errorf("db: chain lock %q does not exist (EnsureChainLock was not run)", name)
	}
	return nil
}
