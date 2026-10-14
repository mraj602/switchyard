// Package jsonedit edits a JSON document by byte-span splice: only the bytes of the edited
// value (or the inserted/removed member and its separator) change, so key order, number
// spelling, indentation and every other value stay exactly as they were. It is how sy edits
// settings.json (setup.md 5.5) and the mcpServers key of a .claude.json (5.4), which holds
// a login that must never be rewritten as a whole.
//
// edit.go is the pure editor over a byte slice; file.go applies it to a file (resolved
// symlink target, mode kept, temp+rename) and adds the .claude.json mode: Claude Code's lock
// directory, the live-session skip and the post-edit checks.
//
// Phase 3 wave 1, WP-C. Depends on: nothing outside the standard library.
package jsonedit
