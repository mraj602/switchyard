// Package termctl is the terminal control side of phase 7 (docs/spec/terminal.md T6, T7):
// tab titles as Claude Code `terminalSequence` hook output, the focus-URL allow-list, and
// the OS opener that brings a Warp tab forward. It never types into a terminal and never
// runs a shell: the opener gets an argv.
//
// Phase 7. Wave 0 holds the Opener interface; WP-F owns the rest of this package (title
// format, allow-list, opener_<os>.go, the fakeopen seam). Folders and environment come from
// termenv.Env (internal/termenv/lint_test.go).
//
// Depends on: termenv.
package termctl

import "context"

// Opener opens one URL that has already passed the T7 allow-list (a Warp focus URL, or a
// `warp://tab_config/switchyard-<label>` URI for a file sy wrote). Implementations run the
// OS opener with an argv and a 5 s timeout: xdg-open on Linux, open on macOS,
// rundll32.exe url.dll,FileProtocolHandler on Windows. Tests use a fake that records the
// URL; release builds never contain the SY_TEST_OPENER file seam.
type Opener interface {
	Open(ctx context.Context, url string) error
}
