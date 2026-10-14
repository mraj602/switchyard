// Package config reads and writes config.toml (hub address, privacy, hooks, status line,
// outbox and slot settings), keeping unknown keys on rewrite.
//
// Contract: docs/spec/paths.md section 4.
//
// Phase 1. Depends on: paths.
package config
