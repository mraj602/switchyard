// Package clock is the engine's time source: Now and one-shot timers, with a manual Fake
// for tests (push.md 10, 11.1). Real time passes through Real; the scheduler never calls
// package time directly, so the fake can drive it deterministically.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Timer is a one-shot timer.
type Timer interface {
	// C delivers the time the timer fired, once.
	C() <-chan time.Time
	// Stop prevents the timer from firing; it reports whether it was still pending.
	Stop() bool
	// Reset re-arms the timer to fire after d from now; it reports whether it was pending.
	Reset(d time.Duration) bool
}

// Clock is the time source.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Real is the wall clock.
type Real struct{}

// Now returns time.Now (with its monotonic reading).
func (Real) Now() time.Time { return time.Now() }

// NewTimer wraps time.NewTimer.
func (Real) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

// Fake is a manually advanced clock. Timers fire when Advance (or Set) moves time past
// them, in order of due time (ties in creation order). Jump moves only the wall clock, as
// a laptop resume or a clock step does, leaving timers' monotonic schedule alone.
//
// Fake keeps two readings: Now is the wall reading (Advance and Jump both move it) and
// Mono is the monotonic reading (only Advance moves it). Timers run on the monotonic
// reading, so a Jump does not fire any timer, which is what the scheduler's jump detection
// has to cope with.
type Fake struct {
	mu     sync.Mutex
	wall   time.Time
	mono   time.Duration // elapsed monotonic time since creation
	timers []*fakeTimer
	seq    int
}

// NewFake starts a fake clock at start.
func NewFake(start time.Time) *Fake { return &Fake{wall: start} }

// Now is the wall clock reading. The returned time carries no monotonic reading, like a
// time read after a wall-clock step.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wall
}

// Mono is the monotonic time elapsed since the clock was created; it is not moved by Jump.
func (f *Fake) Mono() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mono
}

// NewTimer creates a timer that fires after d of monotonic time.
func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{f: f, c: make(chan time.Time, 1)}
	t.arm(d)
	return t
}

// Pending is the number of armed timers.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.timers {
		if t.armed {
			n++
		}
	}
	return n
}

// NextTimer reports the monotonic delay until the earliest armed timer.
func (f *Fake) NextTimer() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *fakeTimer
	for _, t := range f.timers {
		if t.armed && (best == nil || t.less(best)) {
			best = t
		}
	}
	if best == nil {
		return 0, false
	}
	return best.at - f.mono, true
}

// Advance moves both readings forward by d, firing every timer that falls due, in order.
// Each fires with the wall time of its due instant, and the clock reads that instant while
// the timer fires, so a callback that reads Now sees a consistent time. Negative d is a no-op.
func (f *Fake) Advance(d time.Duration) {
	if d < 0 {
		return
	}
	f.mu.Lock()
	target := f.mono + d
	for {
		var next *fakeTimer
		for _, t := range f.timers {
			if t.armed && t.at <= target && (next == nil || t.less(next)) {
				next = t
			}
		}
		if next == nil {
			break
		}
		step := next.at - f.mono
		f.wall = f.wall.Add(step)
		f.mono = next.at
		next.armed = false
		select {
		case next.c <- f.wall:
		default:
		}
	}
	f.wall = f.wall.Add(target - f.mono)
	f.mono = target
	f.prune()
	f.mu.Unlock()
}

// Jump moves only the wall clock by d (positive or negative), simulating a suspend/resume
// or a clock step. No timer fires.
func (f *Fake) Jump(d time.Duration) {
	f.mu.Lock()
	f.wall = f.wall.Add(d)
	f.mu.Unlock()
}

// Set moves the wall clock to t and advances monotonic time by the difference when t is in
// the future (so timers fire); a t in the past behaves like Jump.
func (f *Fake) Set(t time.Time) {
	d := t.Sub(f.Now())
	if d > 0 {
		f.Advance(d)
		return
	}
	f.Jump(d)
}

// prune drops stopped and fired timers that are not armed (f.mu held).
func (f *Fake) prune() {
	kept := f.timers[:0]
	for _, t := range f.timers {
		if t.armed {
			kept = append(kept, t)
		}
	}
	f.timers = kept
	sort.SliceStable(f.timers, func(i, j int) bool { return f.timers[i].less(f.timers[j]) })
}

type fakeTimer struct {
	f     *Fake
	c     chan time.Time
	at    time.Duration // monotonic due time
	seq   int
	armed bool
}

func (t *fakeTimer) less(o *fakeTimer) bool {
	if t.at != o.at {
		return t.at < o.at
	}
	return t.seq < o.seq
}

// arm schedules the timer (f.mu held).
func (t *fakeTimer) arm(d time.Duration) {
	if d < 0 {
		d = 0
	}
	t.f.seq++
	t.seq = t.f.seq
	t.at = t.f.mono + d
	t.armed = true
	for _, o := range t.f.timers {
		if o == t {
			return
		}
	}
	t.f.timers = append(t.f.timers, t)
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	was := t.armed
	t.armed = false
	return was
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	was := t.armed
	if !was {
		t.arm(d)
		return false
	}
	// still in the list: just move it
	if d < 0 {
		d = 0
	}
	t.f.seq++
	t.seq = t.f.seq
	t.at = t.f.mono + d
	return true
}
