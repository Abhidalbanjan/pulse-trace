// Package sqlite registers the pure-Go SQLite driver.
//
// # Why this is a separate package
//
// `shared/db` used to blank-import both drivers itself. That made every service
// importing the dialect layer link SQLite — including the cluster services that
// will never open a SQLite file. modernc.org/sqlite is a translated C library
// and not small, and binary size is D1, the dimension the whole P1 phase exists
// to improve. Linking a database engine into eight binaries to be used by one
// is the wrong direction.
//
// So the driver lives behind an import the binary chooses:
//
//	import _ "github.com/pulsetrace/shared/db/driver/sqlite"
//
// The lite binary imports it. Cluster services do not, and db.Open returns a
// clear error naming this package if a SQLite DSN reaches one that did not.
package sqlite

import _ "modernc.org/sqlite" // registers the "sqlite" driver
