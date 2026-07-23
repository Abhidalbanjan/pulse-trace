// Package migrate is a minimal, dependency-free forward-only SQL migration
// runner. It applies embedded *.sql files against a database/sql connection
// and records which have run in a per-service tracking table, so the several
// services that share one PostgreSQL database can each own and apply their
// own migrations without colliding on a single shared version table.
//
// It intentionally takes an already-open *sql.DB rather than importing any
// driver, so callers keep whatever driver they already use (gateway-service
// uses lib/pq; the pgx-based services open a short-lived database/sql handle
// via shared/db.OpenSQLForMigrations). Each migration file is executed as a
// single multi-statement Exec inside its own transaction, so a file that
// fails leaves no partial state.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"
)

// Run applies every *.sql file in files (embedded at the FS root via
// //go:embed *.sql) that has not already been applied for this service, in
// lexical filename order. version is the filename without the .sql suffix, so
// the conventional NNN_description.sql naming gives a deterministic order.
//
// Applied versions are tracked in schema_migrations_<service>. Because the
// project's migration files are written idempotently (CREATE TABLE IF NOT
// EXISTS / INSERT ... ON CONFLICT DO NOTHING), running against a database that
// already has the objects is safe: nothing is duplicated and the versions are
// simply recorded.
func Run(ctx context.Context, db *sql.DB, service string, files fs.FS) error {
	table := "schema_migrations_" + sanitizeIdent(service)

	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`, table)); err != nil {
		return fmt.Errorf("migrate[%s]: create tracking table: %w", service, err)
	}

	applied := make(map[string]bool)
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT version FROM %s", table))
	if err != nil {
		return fmt.Errorf("migrate[%s]: load applied versions: %w", service, err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("migrate[%s]: scan version: %w", service, err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("migrate[%s]: iterate versions: %w", service, err)
	}
	rows.Close()

	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return fmt.Errorf("migrate[%s]: read migrations: %w", service, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	newlyApplied := 0
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		if applied[version] {
			continue
		}

		content, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("migrate[%s]: read %s: %w", service, name, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migrate[%s]: begin tx for %s: %w", service, name, err)
		}
		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate[%s]: apply %s: %w", service, name, err)
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("INSERT INTO %s (version) VALUES ($1)", table), version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate[%s]: record %s: %w", service, name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migrate[%s]: commit %s: %w", service, name, err)
		}
		log.Printf("migrate[%s]: applied %s", service, name)
		newlyApplied++
	}

	if newlyApplied == 0 {
		log.Printf("migrate[%s]: schema up to date (%d migrations)", service, len(names))
	} else {
		log.Printf("migrate[%s]: applied %d new migration(s), %d total", service, newlyApplied, len(names))
	}
	return nil
}

// sanitizeIdent reduces an arbitrary service name to a safe SQL identifier
// fragment (letters, digits, underscore) so it can be interpolated into the
// tracking table name. Service names are compile-time constants passed by the
// services themselves, not user input, but this keeps the interpolation
// unambiguously safe.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r == '-':
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "service"
	}
	return b.String()
}
