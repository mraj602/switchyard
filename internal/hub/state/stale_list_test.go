package state

import (
	"testing"
	"time"
)

// A session_list snapshot taken before a hook event but applied after it (arrival order is
// not event order) must not undo the event: no busy/idle repair from a stale snapshot.
func TestStaleListDoesNotRepair(t *testing.T) {
	sec := time.Second
	var s Session
	s.Apply(Input{ID: "1", TS: t0, Hook: h("SessionStart")})
	s.Apply(Input{ID: "2", TS: t0.Add(20 * sec), Hook: h("UserPromptSubmit")}) // working
	// A list taken at +10 s says idle; it is applied now, after the +20 s event.
	s.Apply(Input{ID: "3", TS: t0.Add(10 * sec), List: list(true, true, "idle")})
	if s.State != Working {
		t.Fatalf("stale idle snapshot repaired the state to %s", s.State)
	}
	// The same for busy against a Stop that came later.
	var d Session
	d.Apply(Input{ID: "1", TS: t0, Hook: h("UserPromptSubmit")})
	d.Apply(Input{ID: "2", TS: t0.Add(20 * sec), Hook: h("Stop")}) // done
	d.Apply(Input{ID: "3", TS: t0.Add(10 * sec), List: list(true, true, "busy")})
	if d.State != Done {
		t.Fatalf("stale busy snapshot repaired the state to %s", d.State)
	}
	// A list taken after the last event still repairs.
	d.Apply(Input{ID: "4", TS: t0.Add(30 * sec), List: list(true, true, "busy")})
	if d.State != Working || d.Detail != "repaired" {
		t.Fatalf("fresh snapshot: %s:%s", d.State, d.Detail)
	}
}
