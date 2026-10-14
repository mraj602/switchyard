//go:build unix

package tui

import (
	"context"
	"syscall"
	"testing"
	"time"
)

func TestWatchResizeSIGWINCH(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := WatchResize(ctx, func() (int, int) { return 80, 24 })
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no resize tick after SIGWINCH")
	}
}
