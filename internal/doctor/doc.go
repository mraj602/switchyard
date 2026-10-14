// Package doctor implements `sy doctor`: checks that Claude Code, slots, hooks, the
// status line, the timer, the outbox and (from phase 2) the hub connection and token are
// in order, printing one line of fix for each failure.
//
// Phase 1 (first checks), phase 3 (full), phase 4 (push checks on the hub, read-only).
// Depends on: paths, config, identity, outbox, client, and the hub database file (opened
// read-only with the SQLite driver; never internal/hub/api).
package doctor
