// Package migrations embeds the SQL schema so binaries and tests can apply it
// without depending on a filesystem path.
package migrations

import _ "embed"

// SchemaSQL is the full initial schema (idempotent CREATE IF NOT EXISTS).
//
//go:embed 001_init.sql
var SchemaSQL string
