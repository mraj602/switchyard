//go:build !unix

package tui

import (
	"context"
	"time"
)

// PollInterval is how often the terminal size is polled where there is no SIGWINCH
// (Windows: every 500 ms, docs/spec/terminal.md T4).
var PollInterval = 500 * time.Millisecond

// WatchResize sends on the returned channel when size() changes, polled every PollInterval,
// until ctx ends.
func WatchResize(ctx context.Context, size func() (int, int)) <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		t := time.NewTicker(PollInterval)
		defer t.Stop()
		c, r := size()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if nc, nr := size(); nc != c || nr != r {
					c, r = nc, nr
					select {
					case out <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return out
}
