package migrations

import "embed"

// FS holds this service's schema migrations, applied at startup by
// shared/migrate.Run against the shared PostgreSQL database. Files are named
// NNN_description.sql and applied in lexical order.
//
//go:embed *.sql
var FS embed.FS
