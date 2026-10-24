//go:build !fakeclock

package cli

import (
	"github.com/mraj602/watchyard/internal/hub/push/clock"
	"github.com/mraj602/watchyard/internal/hub/push/engine"
	"github.com/mraj602/watchyard/internal/hub/push/refresh"
)

// newServeHooks: a normal build always runs on the wall clock.
func newServeHooks(func(string, ...any)) (serveHooks, error) {
	return serveHooks{Clock: clock.Real{}, Attach: func(*engine.Engine) {},
		AttachRefresh: func(*refresh.Refresher) {}, Close: func() {}}, nil
}
