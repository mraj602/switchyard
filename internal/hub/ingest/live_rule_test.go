package ingest_test

import (
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/record"
)

// counts.live and Session.dormant follow one rule, the board's isLive (states.md 5): open,
// and needs you, or any state but done unless stale, or done with activity in the last 12 h.
func TestLiveRule(t *testing.T) {
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("laptop", m)
	const (
		done    = "dddddddd-0000-4000-8000-000000000001"
		working = "dddddddd-0000-4000-8000-000000000002"
		failed  = "dddddddd-0000-4000-8000-000000000003"
	)
	f.now = t0
	ev := func(sid, name string) {
		f.ingest(d, hook(t, m, t0, `{"hook_event_name":"`+name+`","session_id":"`+sid+`","cwd":"/home/me/x"}`))
	}
	ev(done, "UserPromptSubmit")
	ev(done, "Stop")
	ev(working, "UserPromptSubmit")
	ev(failed, "UserPromptSubmit")
	f.ingest(d, hook(t, m, t0.Add(time.Second), `{"hook_event_name":"StopFailure","error":"overloaded","session_id":"`+failed+`","cwd":"/home/me/x"}`))
	if v := f.session(failed); v.State != "failed" {
		t.Fatalf("failed session is %s", v.State)
	}
	for _, tc := range []struct {
		after                    time.Duration
		live                     int
		doneD, workingD, failedD bool // dormant
	}{
		{time.Hour, 3, false, false, false},
		{11 * time.Hour, 3, false, false, false},
		// 13 h: the done session is quiet, the working one (no lists) is stale; failed stays.
		{13 * time.Hour, 1, true, true, false},
		{72 * time.Hour, 1, true, true, false},
	} {
		at := t0.Add(tc.after)
		f.now = at
		c, err := f.st.Counts(f.ctx, at)
		if err != nil {
			t.Fatal(err)
		}
		if c.Live != tc.live {
			t.Errorf("+%v: live %d, want %d (%+v)", tc.after, c.Live, tc.live, c)
		}
		for sid, want := range map[string]bool{done: tc.doneD, working: tc.workingD, failed: tc.failedD} {
			if v := f.session(sid); v.Dormant != want {
				t.Errorf("+%v: %s (%s, stale %v) dormant %v, want %v", tc.after, sid[len(sid)-1:], v.State, v.Stale, v.Dormant, want)
			}
		}
	}
}
