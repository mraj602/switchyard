package main

import (
	"errors"
	"fmt"
)

// Phase 5 command stubs (docs/phases/5.md wave 0). Each answers "not implemented in this
// build yet (phase 5)" and exits 3, so scripts and `sy machine add`'s remote calls can tell
// "this sy is too old or too early" from a real failure. The owner work package of each
// command replaces its stub: update.go (WP-D), directpush.go (WP-C), the `sy machine`
// subcommands in machine.go (WP-A), `sy outbox drain` in outbox.go (WP-B).

// exitNotImplemented is the exit status of a stub (the status placeholder commands use).
const exitNotImplemented = 3

var errPhase5 = errors.New("not implemented in this build yet (phase 5)")

// errNotImplemented is what a stub returns for `<what>`; dispatch prints
// "sy <command>: <what>: not implemented in this build yet (phase 5)" and exits 3.
func errNotImplemented(what string) error {
	return exitWith(exitNotImplemented, fmt.Errorf("%s: %w", what, errPhase5))
}
