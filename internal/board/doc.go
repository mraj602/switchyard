// Package board builds the session board's view model, the versioned View of
// docs/spec/terminal.md T2, from either source: the hub's read API (T3, internal/board/hubsrc)
// or this machine's outbox folded with the same code as `sy status` (local.go). The two
// renderers, `sy board` (internal/board/tui) and the pane mod (plugin/switchyard-pane), only
// lay a View out; every word, age, state label and ordering rule is computed here, once.
//
// Rules for this package and its sub-packages:
//   - Folders and environment come from an injected termenv.Env; nothing here reads HOME or
//     calls os.UserHomeDir (internal/termenv/lint_test.go).
//   - Read only: no code path writes to a session, sends keystrokes or calls /v1/ingest.
//   - Every string from a record is made terminal-safe before it enters a View (T2 text
//     safety).
//
// Phase 7. Wave 0 holds the types (view.go); WP-A owns build, words, text, local and order;
// WP-B the hub source and the board token; WP-C the TUI.
//
// Depends on: termenv (and, from wave 1, status, hub/state for the pure fold).
package board
