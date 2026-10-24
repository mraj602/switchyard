package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/outbox"
)

// Peek reads (and may quarantine corrupt lines) only as the flush.lock
// holder, so it refuses while a flush or drain holds the lock.
func TestOutboxDrainPeekTakesTheFlushLock(t *testing.T) {
	_, dir := drainEnv(t)
	ob := outbox.New(dir)
	lock, ok, err := ob.TryFlushLock()
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var out, errb bytes.Buffer
	err = runOutboxIn(nil, []string{"drain", "--peek", "--dir", dir}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "flush.lock") || out.Len() != 0 {
		t.Fatalf("peek under a held lock: %v %q", err, out.String())
	}
	lock.Release()
	if err := runOutboxIn(nil, []string{"drain", "--peek", "--dir", dir}, &out, &errb); err != nil {
		t.Fatal(err, errb.String())
	}
	// and the lock is free again afterwards
	l2, ok, err := ob.TryFlushLock()
	if err != nil || !ok {
		t.Fatalf("peek kept the lock: %v %v", ok, err)
	}
	l2.Release()
}
