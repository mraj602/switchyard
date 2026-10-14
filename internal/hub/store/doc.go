// Package store owns the hub's SQLite database (modernc.org/sqlite, pure Go, WAL mode):
// opening, migrations from docs/spec/schema.sql, backup and prune. Every other hub
// package reads and writes only through it.
//
// Contract: docs/spec/schema.sql.
//
// Phase 2. Depends on: nothing inside the module.
package store
