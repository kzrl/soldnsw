package database

import _ "embed"

// SchemaSQL is the SQLite DDL for the application. Keeping it next to the
// sqlc-generated code means the schema is embedded in the binaries and does
// not depend on the process working directory.
//
//go:embed schema.sql
var SchemaSQL string
