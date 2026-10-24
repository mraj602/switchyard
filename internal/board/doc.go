// Package board builds the session board's view model, the versioned View of
// docs/spec/terminal.md T2, from either source: the hub's read API (T3, internal/board/hubsrc)
// or this machine's outbox folded with the same code as `wy status` (local.go). The two
// renderers, `wy board` (internal/board/tui) and the pane mod (plugin/watchyard-pane), only
// lay a View out; every word, age, state label and ordering rule is computed here, once.
//
// Rules for this package and its sub-packages:
//   - Folders and environment come from an injected termenv.Env; nothing here reads HOME or
//     calls os.UserHomeDir (internal/termenv/lint_test.go).
//   - Read only: no code path writes to a session, sends keystrokes or calls /v1/ingest.
//   - Every string from a record is made terminal-safe before it enters a View (T2 text
//     safety).
//
// The types are in view.go; build, words, text, local and order compute them, hubsrc is the
// hub source and the board token, tui the screen.
//
// Depends on: termenv, status, hub/state (for the pure fold).
package board
