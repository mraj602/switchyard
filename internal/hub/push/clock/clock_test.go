package clock

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)

func fired(tm Timer) (time.Time, bool) {
	select {
	case at := <-tm.C():
		return at, true
	default:
		return time.Time{}, false
	}
}

func TestFakeTimersFireInOrder(t *testing.T) {
	f := NewFake(t0)
	a := f.NewTimer(30 * time.Second)
	b := f.NewTimer(10 * time.Second)
	c := f.NewTimer(30 * time.Second) // ties fire in creation order, after b
	if f.Pending() != 3 {
		t.Fatal("pending")
	}
	f.Advance(9 * time.Second)
	if _, ok := fired(b); ok {
		t.Fatal("b early")
	}
	f.Advance(1 * time.Second)
	at, ok := fired(b)
	if !ok || !at.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("b: %v %v", at, ok)
	}
	if _, ok := fired(a); ok {
		t.Fatal("a early")
	}
	f.Advance(time.Minute)
	atA, okA := fired(a)
	atC, okC := fired(c)
	if !okA || !okC || !atA.Equal(t0.Add(30*time.Second)) || !atC.Equal(atA) {
		t.Fatalf("a %v c %v", atA, atC)
	}
	if !f.Now().Equal(t0.Add(70 * time.Second)) {
		t.Fatalf("now %v", f.Now())
	}
	if f.Pending() != 0 {
		t.Fatal("pending after")
	}
}

func TestFakeStopAndReset(t *testing.T) {
	f := NewFake(t0)
	tm := f.NewTimer(time.Minute)
	if !tm.Stop() || tm.Stop() {
		t.Fatal("stop result")
	}
	f.Advance(2 * time.Minute)
	if _, ok := fired(tm); ok {
		t.Fatal("stopped timer fired")
	}
	if tm.Reset(10 * time.Second) {
		t.Fatal("reset of a stopped timer reports pending")
	}
	if d, ok := f.NextTimer(); !ok || d != 10*time.Second {
		t.Fatalf("next %v %v", d, ok)
	}
	if !tm.Reset(5 * time.Second) {
		t.Fatal("reset of a pending timer")
	}
	f.Advance(5 * time.Second)
	if _, ok := fired(tm); !ok {
		t.Fatal("reset timer did not fire")
	}
}

// Jump moves the wall clock, not the monotonic schedule: no timer fires, and the drift
// between Now and Mono is what a scheduler uses to detect a suspend.
func TestFakeJump(t *testing.T) {
	f := NewFake(t0)
	tm := f.NewTimer(time.Minute)
	f.Jump(2 * time.Hour)
	if _, ok := fired(tm); ok {
		t.Fatal("jump fired a timer")
	}
	if !f.Now().Equal(t0.Add(2*time.Hour)) || f.Mono() != 0 {
		t.Fatalf("now %v mono %v", f.Now(), f.Mono())
	}
	f.Advance(time.Minute)
	if _, ok := fired(tm); !ok {
		t.Fatal("timer lost after jump")
	}
	if !f.Now().Equal(t0.Add(2*time.Hour + time.Minute)) {
		t.Fatalf("now %v", f.Now())
	}
	f.Jump(-time.Hour) // clock stepped back
	if !f.Now().Equal(t0.Add(time.Hour + time.Minute)) {
		t.Fatalf("now %v", f.Now())
	}
}

func TestFakeSetAndRealClock(t *testing.T) {
	f := NewFake(t0)
	tm := f.NewTimer(time.Minute)
	f.Set(t0.Add(time.Hour))
	if _, ok := fired(tm); !ok || !f.Now().Equal(t0.Add(time.Hour)) {
		t.Fatal("set forward")
	}
	var c Clock = Real{}
	rt := c.NewTimer(time.Millisecond)
	select {
	case <-rt.C():
	case <-time.After(2 * time.Second):
		t.Fatal("real timer")
	}
	if time.Since(c.Now()) > time.Minute {
		t.Fatal("real now")
	}
	var _ Clock = f
}
