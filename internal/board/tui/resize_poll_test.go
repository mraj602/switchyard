//go:build !unix

package tui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchResizePolls(t *testing.T) {
	old := PollInterval
	PollInterval = 10 * time.Millisecond
	defer func() { PollInterval = old }()
	var cols atomic.Int32
	cols.Store(80)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := WatchResize(ctx, func() (int, int) { return int(cols.Load()), 24 })
	time.Sleep(30 * time.Millisecond)
	cols.Store(100)
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no tick after a size change")
	}
}
