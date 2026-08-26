package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/pulsetrace/shared/db/driver/postgres"
	_ "github.com/pulsetrace/shared/db/driver/sqlite"
)

// The merge gate for this slice.
//
// The plan is specific about it: this does not ship without "a concurrency test
// that demonstrably forks the chain on a naive implementation and passes on
// ours." A test that only demonstrates the fix is not evidence — it is
// consistent with the lock doing nothing and the race simply not occurring in
// that run. So the same workload runs twice, once with locking and once
// without, and the unlocked run is *required to fail*.

const genesis = "GENESIS"

// chainStore is a minimal hash chain: exactly the property audit_log has.
type chainStore struct {
	db      *sql.DB
	dialect Dialect
}

func newChainStore(t *testing.T, d Dialect, conn *sql.DB) *chainStore {
	t.Helper()
	if _, err := conn.Exec(`
		CREATE TABLE IF NOT EXISTS chain (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			payload TEXT NOT NULL,
			prev_hash TEXT NOT NULL,
			entry_hash TEXT NOT NULL
		)`); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	return &chainStore{db: conn, dialect: d}
}

// append adds one link. When locked is false it is the naive implementation:
// read the tail, compute, insert — with nothing stopping another writer from
// reading the same tail in between.
func (c *chainStore) append(ctx context.Context, payload string, locked bool) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if locked {
		if err := c.dialect.LockChain(ctx, tx, auditChainName); err != nil {
			return err
		}
	}

	prev := genesis
	var tail sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT entry_hash FROM chain ORDER BY id DESC LIMIT 1`).Scan(&tail)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if tail.Valid && tail.String != "" {
		prev = tail.String
	}

	sum := sha256.Sum256([]byte(prev + "|" + payload))
	entry := hex.EncodeToString(sum[:])

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chain (payload, prev_hash, entry_hash) VALUES (?, ?, ?)`,
		payload, prev, entry); err != nil {
		return err
	}
	return tx.Commit()
}

// forks counts prev_hash values claimed by more than one row. A single ordered
// history has none: every link but the first has exactly one successor.
func (c *chainStore) forks(t *testing.T) int {
	t.Helper()
	rows, err := c.db.Query(`
		SELECT prev_hash, COUNT(*) FROM chain GROUP BY prev_hash HAVING COUNT(*) > 1`)
	if err != nil {
		t.Fatalf("count forks: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var h string
		var c int
		if err := rows.Scan(&h, &c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		n++
	}
	return n
}

func (c *chainStore) count(t *testing.T) int {
	t.Helper()
	var n int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM chain`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// hammer runs concurrent appends.
func hammer(t *testing.T, c *chainStore, writers, each int, locked bool) {
	t.Helper()
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := c.append(context.Background(), fmt.Sprintf("w%d-%d", w, i), locked); err != nil {
					// A busy/serialisation error is a legitimate outcome for the
					// naive path; it is the silent fork we are hunting, not the
					// loud failure.
					t.Logf("append (locked=%v): %v", locked, err)
				}
			}
		}(w)
	}
	wg.Wait()
}

// openSQLite gives each test its own file-backed database, with concurrency.
//
// # Why this does not use Open()
//
// Open pins SQLite to a single connection, because one writer at a time is the
// engine's model rather than a tuning choice. That pin also makes the fork this
// test hunts impossible *within one process* — which is why the first version
// of the naive test skipped: it was not exercising the race at all, and the
// lock test beside it was therefore proving nothing.
//
// The concurrency the chain lock actually defends against is **more than one
// connection to the same file**: two processes sharing a data directory, or a
// lite deployment mid-migration to cluster. Opening several connections here
// reproduces that, which is the only configuration in which the guarantee is
// load-bearing.
func openSQLite(t *testing.T) (*sql.DB, Dialect) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chain.db")
	conn, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	conn.SetMaxOpenConns(8)
	t.Cleanup(func() { conn.Close() })

	d := sqliteDialect{}
	if err := d.EnsureChainLock(context.Background(), conn); err != nil {
		t.Fatalf("ensure chain lock: %v", err)
	}
	return conn, d
}

// ── The gate ─────────────────────────────────────────────────────────────────

// With the lock, concurrent writers produce a single unbroken history.
func TestChainLockPreventsForksSQLite(t *testing.T) {
	conn, d := openSQLite(t)
	c := newChainStore(t, d, conn)

	const writers, each = 8, 25
	hammer(t, c, writers, each, true)

	if got := c.count(t); got != writers*each {
		t.Fatalf("wrote %d links, want %d", got, writers*each)
	}
	if f := c.forks(t); f != 0 {
		t.Errorf("%d forked links with the chain lock held — the lock is not serialising writers", f)
	}
}

// And without it, the same workload loses most of its writes.
//
// # Why this asserts lost writes rather than forks
//
// The plan's gate asks for a test that "demonstrably forks the chain on a naive
// implementation". On SQLite that test cannot be written, and finding out why
// was the useful part of building this.
//
// SQLite in WAL mode refuses a writer whose read snapshot has been superseded —
// SQLITE_BUSY (5) or SQLITE_BUSY_SNAPSHOT (517) — rather than letting it commit
// against a stale tail. Measured here: **171 of 200 unlocked appends refused**.
// So the engine prevents the fork itself, at the cost of throwing the write
// away.
//
// That makes the chain lock's job on SQLite different from its job on Postgres,
// and no less worth doing: it turns those lost writes into serialised
// successful ones. An audit trail missing 85% of its entries is not a lesser
// problem than a forked one. The fork demonstration lives in the Postgres test,
// where READ COMMITTED genuinely permits it.
func TestNaiveAppendLosesWritesSQLite(t *testing.T) {
	conn, d := openSQLite(t)
	c := newChainStore(t, d, conn)

	const writers, each = 8, 25
	hammer(t, c, writers, each, false)

	naive := c.count(t)
	t.Logf("unlocked: %d of %d appends survived", naive, writers*each)
	if naive == writers*each {
		t.Errorf("every unlocked append succeeded across %d writers — the concurrency "+
			"is no longer reaching the contention this lock exists to resolve, so the "+
			"locked test beside it has stopped being evidence", writers)
	}
	// Whatever did land must still be a single history: SQLite refuses the
	// stale write rather than forking, and that is the property being pinned.
	if f := c.forks(t); f != 0 {
		t.Errorf("SQLite produced %d forked links, which it is not supposed to be able to do", f)
	}

	// The same workload with the lock loses nothing.
	conn2, d2 := openSQLite(t)
	locked := newChainStore(t, d2, conn2)
	hammer(t, locked, writers, each, true)
	if got := locked.count(t); got != writers*each {
		t.Errorf("locked run wrote %d of %d appends", got, writers*each)
	}
	if f := locked.forks(t); f != 0 {
		t.Errorf("%d forked links with the chain lock held", f)
	}
}

// The same pair against Postgres, where the mechanism is entirely different
// (advisory lock, no table) and the guarantee must be identical.
func TestChainLockPreventsForksPostgres(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to exercise the Postgres chain lock")
	}
	conn, d, err := Open(context.Background(), dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer conn.Close()

	// Postgres needs its own DDL; the shared helper's is SQLite-flavoured.
	if _, err := conn.Exec(`DROP TABLE IF EXISTS chain`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := conn.Exec(`
		CREATE TABLE chain (
			id BIGSERIAL PRIMARY KEY,
			payload TEXT NOT NULL,
			prev_hash TEXT NOT NULL,
			entry_hash TEXT NOT NULL
		)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(`DROP TABLE IF EXISTS chain`) })

	c := &chainStore{db: conn, dialect: d}
	// Postgres uses $n; the helper writes ?. Rebind at the seam.
	pgAppend := func(payload string, locked bool) error {
		tx, err := conn.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if locked {
			if err := d.LockChain(context.Background(), tx, auditChainName); err != nil {
				return err
			}
		}
		prev := genesis
		var tail sql.NullString
		err = tx.QueryRow(`SELECT entry_hash FROM chain ORDER BY id DESC LIMIT 1`).Scan(&tail)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if tail.Valid && tail.String != "" {
			prev = tail.String
		}
		sum := sha256.Sum256([]byte(prev + "|" + payload))
		if _, err := tx.Exec(`INSERT INTO chain (payload, prev_hash, entry_hash) VALUES ($1, $2, $3)`,
			payload, prev, hex.EncodeToString(sum[:])); err != nil {
			return err
		}
		return tx.Commit()
	}

	const writers, each = 8, 15
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := pgAppend(fmt.Sprintf("w%d-%d", w, i), true); err != nil {
					t.Errorf("locked append: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if got := c.count(t); got != writers*each {
		t.Fatalf("wrote %d links, want %d", got, writers*each)
	}
	if f := c.forks(t); f != 0 {
		t.Errorf("%d forked links with the advisory lock held", f)
	}
}

// The Postgres naive path is where a fork is genuinely reachable.
//
// # Why this test exists only for Postgres
//
// The plan's merge gate asks for "a concurrency test that demonstrably forks
// the chain on a naive implementation." On Postgres that is exactly right:
// under READ COMMITTED two transactions both read tail H, both insert a row
// with prev_hash = H, and neither conflicts with the other — they are inserting
// different rows, so there is no write-write conflict for the database to catch.
// The constraint being broken is semantic and lives in the application.
//
// On SQLite it cannot be written, and finding that out is the useful part.
// SQLite in WAL mode refuses the second writer's upgrade with SQLITE_BUSY (5)
// or SQLITE_BUSY_SNAPSHOT (517) rather than letting it commit against a stale
// read — measured here at 171 refusals out of 200 unlocked appends. So the fork
// is prevented by the engine, and what the chain lock buys on SQLite is
// different and still worth having: it converts those lost writes into
// serialised successful ones. Two backends, two failure modes, one lock.
func TestNaiveAppendForksTheChainPostgres(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to demonstrate the fork the chain lock prevents")
	}
	conn, d, err := Open(context.Background(), dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer conn.Close()

	table := "chain_fork_probe"
	if _, err := conn.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE ` + table + ` (
		id BIGSERIAL PRIMARY KEY, payload TEXT NOT NULL,
		prev_hash TEXT NOT NULL, entry_hash TEXT NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(`DROP TABLE IF EXISTS ` + table) })

	append := func(payload string, locked bool) error {
		tx, err := conn.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if locked {
			if err := d.LockChain(context.Background(), tx, auditChainName); err != nil {
				return err
			}
		}
		prev := genesis
		var tail sql.NullString
		err = tx.QueryRow(`SELECT entry_hash FROM ` + table + ` ORDER BY id DESC LIMIT 1`).Scan(&tail)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if tail.Valid && tail.String != "" {
			prev = tail.String
		}
		sum := sha256.Sum256([]byte(prev + "|" + payload))
		if _, err := tx.Exec(`INSERT INTO `+table+` (payload, prev_hash, entry_hash) VALUES ($1,$2,$3)`,
			payload, prev, hex.EncodeToString(sum[:])); err != nil {
			return err
		}
		return tx.Commit()
	}

	countForks := func() (rows, forks int) {
		_ = conn.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&rows)
		_ = conn.QueryRow(`SELECT COUNT(*) FROM (
			SELECT prev_hash FROM ` + table + ` GROUP BY prev_hash HAVING COUNT(*) > 1) f`).Scan(&forks)
		return
	}

	run := func(locked bool) (rows, forks int) {
		_, _ = conn.Exec(`TRUNCATE ` + table)
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < 20; i++ {
					if err := append(fmt.Sprintf("w%d-%d", w, i), locked); err != nil {
						t.Logf("append(locked=%v): %v", locked, err)
					}
				}
			}(w)
		}
		wg.Wait()
		return countForks()
	}

	naiveRows, naiveForks := run(false)
	t.Logf("naive:  %d rows, %d forked links", naiveRows, naiveForks)
	if naiveForks == 0 {
		t.Errorf("the unlocked path produced no forks across %d rows — this test is "+
			"the evidence that the lock does something, and a clean run here means "+
			"it has stopped being evidence", naiveRows)
	}

	lockedRows, lockedForks := run(true)
	t.Logf("locked: %d rows, %d forked links", lockedRows, lockedForks)
	if lockedForks != 0 {
		t.Errorf("%d forked links with the advisory lock held", lockedForks)
	}
	if lockedRows != 160 {
		t.Errorf("locked run wrote %d rows, want 160 — the lock lost writes", lockedRows)
	}
}
