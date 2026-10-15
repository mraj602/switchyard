package state

import (
	"testing"
	"time"
)

// states.md 5.1: a transcript observation only makes history of a session no hook event or
// list has seen; newer activity moves ended_at, anything live ignores it.
func TestTranscript(t *testing.T) {
	tr := func(at time.Duration) Input {
		return Input{ID: "t", TS: t0.Add(at), Transcript: &TranscriptObs{}}
	}
	hk := func(at time.Duration, name string) Input { return Input{ID: "h", TS: t0.Add(at), Hook: h(name)} }
	ls := func(at time.Duration, status string) Input {
		return Input{ID: "l", TS: t0.Add(at), List: list(true, true, status)}
	}
	tests := []struct {
		name  string
		in    []Input
		want  string
		ended time.Duration // -1 = not ended
		since time.Duration
	}{
		{"new session is history", []Input{tr(time.Hour)}, "ended:history", time.Hour, time.Hour},
		{"newer activity moves ended_at", []Input{tr(time.Hour), tr(2 * time.Hour)}, "ended:history", 2 * time.Hour, 2 * time.Hour},
		{"older activity is ignored", []Input{tr(2 * time.Hour), tr(time.Hour)}, "ended:history", 2 * time.Hour, 2 * time.Hour},
		{"live session ignores it", []Input{hk(0, "UserPromptSubmit"), tr(time.Hour)}, "working:", -1, 0},
		{"ended session keeps its reason", []Input{hk(0, "SessionEnd"), tr(time.Hour)}, "ended:other", 0, 0},
		{"a hook brings history back", []Input{tr(time.Hour), hk(2*time.Hour, "UserPromptSubmit")}, "working:", -1, 2 * time.Hour},
		{"a listing brings history back", []Input{tr(time.Hour), ls(2*time.Hour, "idle")}, "done:repaired", -1, 2 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s Session
			for _, in := range tc.in {
				s.Apply(in)
			}
			if got := string(s.State) + ":" + s.Detail; got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			if (tc.ended < 0 && !s.EndedAt.IsZero()) || (tc.ended >= 0 && !s.EndedAt.Equal(t0.Add(tc.ended))) {
				t.Errorf("ended_at %v", s.EndedAt)
			}
			if !s.Since.Equal(t0.Add(tc.since)) {
				t.Errorf("since %v", s.Since)
			}
			if s.NeedsYou() {
				t.Error("history needs you")
			}
		})
	}
	// A transcript with no time: ended:history with no times at all.
	var s Session
	s.Apply(Input{ID: "t", Transcript: &TranscriptObs{}})
	if !s.IsHistory() || !s.EndedAt.IsZero() || !s.Since.IsZero() {
		t.Fatalf("%+v", s)
	}
}
