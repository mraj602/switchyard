package cli

import (
	"net/http"

	"github.com/mraj602/watchyard/internal/hub/push/clock"
	"github.com/mraj602/watchyard/internal/hub/push/engine"
	"github.com/mraj602/watchyard/internal/hub/push/refresh"
)

// serveHooks are the seams `wy hub serve` takes from its build: the real clock and nothing
// else in a normal build; a fake clock with a control socket and a test push client in a
// binary built with `-tags fakeclock` (test/e2e/push only, see hooks_fakeclock.go).
type serveHooks struct {
	// Clock is the time source of the engine, the API and the store.
	Clock clock.Clock
	// Fake is true when Clock is a test clock (the store and API then read it too).
	Fake bool
	// WebPushClient replaces the guarded client of the Web Push sender (tests only).
	WebPushClient *http.Client
	// Attach is called with the engine once it is built (nil engine with --no-push).
	Attach func(*engine.Engine)
	// AttachRefresh is called with the widget refresher once it runs (never with --no-push).
	AttachRefresh func(*refresh.Refresher)
	// Close releases what the hooks opened.
	Close func()
}
