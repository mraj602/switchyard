// Package doctor implements `sy doctor`: checks that Claude Code, slots, hooks, the
// status line, the timer, the outbox, the hub connection and token, push and the terminal are
// in order, printing one line of fix for each failure.
//
// Depends on: paths, config, identity, outbox, client, and the hub database file (opened
// read-only with the SQLite driver; never internal/hub/api).
package doctor
