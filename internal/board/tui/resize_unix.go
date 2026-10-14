//go:build unix

package tui

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// WatchResize sends on the returned channel each time the terminal is resized (SIGWINCH)
// until ctx ends. size is unused on Unix; it is the Windows poll's source.
func WatchResize(ctx context.Context, size func() (int, int)) <-chan struct{} {
	out := make(chan struct{}, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(sig)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sig:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
