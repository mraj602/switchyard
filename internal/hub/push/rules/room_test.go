package rules

import (
	"testing"
	"time"
)

// "Who has room" (push.md 5.7): effective use, list order on ties, under 80% only.
func TestWhoHasRoom(t *testing.T) {
	now := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC).UnixMilli()
	later, past := now+int64(2*time.Hour/time.Millisecond), now-int64(time.Minute/time.Millisecond)
	busy := RoomCandidate{Seat: "s-busy", Label: "busy", HasWindow: true, UsedPct: 41, ResetsMS: later, HasReading: true}
	reset := RoomCandidate{Seat: "s-reset", Label: "reset", HasWindow: true, UsedPct: 97, ResetsMS: past, HasReading: true}
	other := RoomCandidate{Seat: "s-other", Label: "other", HasReading: true} // only a 7-day reading
	none := RoomCandidate{Seat: "s-none", Label: "none"}                      // no reading at all
	old := RoomCandidate{Seat: "s-old", Label: "old", HasWindow: true, UsedPct: 12, ResetsMS: later, HasReading: true}

	for _, c := range []struct {
		name string
		c    RoomCandidate
		use  float64
		ok   bool
	}{
		{"current window", busy, 41, true},
		{"reset passed", reset, 0, true},
		{"reset exactly now", RoomCandidate{HasWindow: true, UsedPct: 50, ResetsMS: now, HasReading: true}, 0, true},
		{"no reading for this window, one for another", other, 0, true},
		{"no reading at all", none, 0, false},
	} {
		if use, ok := EffectiveUse(c.c, now); use != c.use || ok != c.ok {
			t.Errorf("%s: EffectiveUse = %v %v, want %v %v", c.name, use, ok, c.use, c.ok)
		}
	}

	pick := func(subject string, cands ...RoomCandidate) string {
		r := WhoHasRoom(subject, cands, now)
		if r == nil {
			return ""
		}
		return r.Label
	}
	// The idle account (reset passed) beats the busier one, whatever the age of its reading.
	if got := pick("s-x", busy, reset); got != "reset" {
		t.Errorf("reset passed: %q", got)
	}
	if r := WhoHasRoom("s-x", []RoomCandidate{busy, reset}, now); r.Left != 100 {
		t.Errorf("an idle account has 100%% left: %v", r.Left)
	}
	// A seat with readings only for another window counts as 0; one with none is skipped.
	if got := pick("s-x", none, busy, other); got != "other" {
		t.Errorf("other window only: %q", got)
	}
	if got := pick("s-x", none); got != "" {
		t.Errorf("no reading at all: %q", got)
	}
	// An old reading still counts until its reset (no 6 h freshness rule any more).
	if got := pick("s-x", busy, old); got != "old" {
		t.Errorf("old reading: %q", got)
	}
	// Ties go to list order.
	if got := pick("s-x", other, reset); got != "other" {
		t.Errorf("tie, first listed: %q", got)
	}
	if got := pick("s-x", reset, other); got != "reset" {
		t.Errorf("tie, first listed: %q", got)
	}
	// The subject and hidden seats are never named; 80% or more never qualifies.
	hidden := reset
	hidden.Hidden = true
	if got := pick("s-reset", reset, busy); got != "busy" {
		t.Errorf("subject named: %q", got)
	}
	if got := pick("s-x", hidden, busy); got != "busy" {
		t.Errorf("hidden named: %q", got)
	}
	full := busy
	full.UsedPct = 80
	if got := pick("s-x", full); got != "" {
		t.Errorf("80%% qualifies: %q", got)
	}
}
