//go:build fakeinbox && peerstest && !windows

package peers_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mraj602/switchyard/internal/peers"
)

// Two `sy peers send` processes (no agent running) must not read limits.json and
// threads.json at the same time: each would start from the same budget and the same turn
// count, so parallel sends would pass the rate limits and the loop guard. Another process's
// lock is stood for by a second open file description, which flock treats the same way.
func TestSendWaitsForAnotherProcessesStateLock(t *testing.T) {
	w := newWorld(t, peers.ModeBypass, peers.ModeBypass)
	w.allow([][]string{{"acc-work", "acc-personal"}}, nil)
	e, c := w.asA()
	p := filepath.Join(w.state, "peers", "state.lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan *peers.Refusal, 1)
	go func() { _, rf := e.Send(c, "worker", "hi"); done <- rf }()
	select {
	case <-done:
		t.Fatal("the send did not wait for the other process's lock")
	case <-time.After(300 * time.Millisecond):
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case rf := <-done:
		if rf != nil {
			t.Fatal(rf)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the send never went ahead")
	}
}
