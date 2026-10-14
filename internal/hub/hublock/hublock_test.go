package hublock

import (
	"errors"
	"testing"
)

func TestAcquireHeldRelease(t *testing.T) {
	dir := t.TempDir()
	if h, err := Held(dir); err != nil || h {
		t.Fatalf("empty folder: held=%v err=%v", h, err)
	}
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h, err := Held(dir); err != nil || !h {
		t.Fatalf("held=%v err=%v", h, err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if h, _ := Held(dir); h {
		t.Fatal("still held after release")
	}
	l2, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()
}
