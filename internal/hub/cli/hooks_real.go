//go:build !fakeclock

package cli

import (
	"github.com/mraj602/switchyard/internal/hub/push/clock"
	"github.com/mraj602/switchyard/internal/hub/push/engine"
	"github.com/mraj602/switchyard/internal/hub/push/expo"
)

// newServeHooks: a normal build always runs on the wall clock.
func newServeHooks(func(string, ...any)) (serveHooks, error) {
	return serveHooks{Clock: clock.Real{}, Attach: func(*engine.Engine) {},
		AttachExpo: func(*expo.Receipts, *expo.Refresher) {}, Close: func() {}}, nil
}
