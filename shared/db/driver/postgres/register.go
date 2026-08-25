// Package postgres registers the pgx stdlib driver.
//
// Separate from shared/db for the same reason its SQLite counterpart is: the
// binary chooses which engines it links, rather than the dialect layer choosing
// for it. See the sqlite package's doc comment.
package postgres

import _ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
