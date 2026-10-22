package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/engine"
)

// A Start that fails leaves no scheduler goroutine behind, so Stop must not wait for one.
func TestStopAfterFailedStart(t *testing.T) {
	h := newHarness(t, noStart())
	eng := engine.New(engine.Config{Store: h.st, Clock: h.clk})
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	if err := eng.Start(ctx); err == nil {
		t.Fatal("Start with a canceled context succeeded")
	}
	done := make(chan struct{})
	go func() { eng.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked after a failed Start")
	}
}
