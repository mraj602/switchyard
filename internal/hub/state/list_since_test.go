package state

import (
	"testing"
	"time"
)

// A list that makes a session done backdates state_since to the session's
// newest activity before the list (ListObs.LastActive, its last hook event, its current
// since), never past the list's ts. A list only sees "idle now"; the time it ran (on a new
// hub: the import) says nothing about when the turn ended.
func TestListDoneSince(t *testing.T) {
	day := 24 * time.Hour
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	ls := func(id string, ts time.Duration, status string, active time.Duration) Input {
		l := list(true, true, status)
		if active != 0 {
			l.LastActive = at(active)
		}
		return Input{ID: id, TS: at(ts), List: l}
	}
	tests := []struct {
		name  string
		in    []Input
		want  string
		since time.Duration
	}{
		{"first seen idle: since its last transcript row", []Input{ls("1", 30*day, "idle", 7*day)}, "done:repaired", 7 * day},
		{"first seen waiting: same", []Input{ls("1", 30*day, "waiting", 7*day)}, "done:repaired", 7 * day},
		{"nothing known: since the list", []Input{ls("1", 30*day, "idle", 0)}, "done:repaired", 30 * day},
		{"activity after the list is not used", []Input{ls("1", 30*day, "idle", 31*day)}, "done:repaired", 30 * day},
		{"first seen busy: working since the list", []Input{ls("1", 30*day, "busy", 7*day)}, "working:repaired", 30 * day},
		{"later lists keep the since", []Input{ls("1", 30*day, "idle", 7*day), ls("2", 30*day+time.Hour, "idle", 7*day)},
			"done:repaired", 7 * day},
		{"busy then idle: since its last row", []Input{ls("1", day, "busy", 0), ls("2", 2*day, "idle", day+time.Hour)},
			"done:repaired", day + time.Hour},
		{"busy then idle with an older row: since working began", []Input{ls("1", day, "busy", 0), ls("2", 2*day, "idle", time.Hour)},
			"done:repaired", day},
		{"missed Stop: the newer of the last hook and row",
			[]Input{{ID: "0", TS: at(time.Hour), Hook: h("UserPromptSubmit")}, ls("1", day, "idle", 2*time.Hour)}, "done:repaired", 2 * time.Hour},
		{"ended session listed again comes back at the list",
			[]Input{{ID: "0", TS: at(time.Hour), Hook: h("SessionEnd")}, ls("1", day, "idle", 2*time.Hour)}, "done:repaired", day},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := Fold(tc.in)
			if got := string(s.State) + ":" + s.Detail; got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			if !s.Since.Equal(at(tc.since)) {
				t.Errorf("since %v, want %v", s.Since, at(tc.since))
			}
			if n := len(s.History); n == 0 || !s.History[n-1].At.Equal(s.Since) {
				t.Errorf("last transition %+v, since %v", s.History, s.Since)
			}
		})
	}
}
