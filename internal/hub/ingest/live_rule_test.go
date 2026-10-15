package ingest_test

import (
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/store"
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

// Each machine counts only its own sessions: the machine list once counted every machine's
// open sessions for every machine (its count subquery's own machine join shadowed the outer
// row), and marked a quiet machine silent because another machine had a working session.
func TestMachineLiveCountsArePerMachine(t *testing.T) {
	f := newFixture(t)
	m1, m2 := "m_"+record.NewID(t0), "m_"+record.NewID(t0.Add(time.Millisecond))
	d1, d2 := f.device("laptop", m1), f.device("server", m2)
	f.now = t0
	ev := func(d store.Device, m, sid, name string) {
		f.ingest(d, hook(t, m, t0, `{"hook_event_name":"`+name+`","session_id":"`+sid+`","cwd":"/home/me/x"}`))
	}
	ev(d1, m1, "eeeeeeee-0000-4000-8000-000000000001", "UserPromptSubmit")
	ev(d1, m1, "eeeeeeee-0000-4000-8000-000000000002", "UserPromptSubmit")
	ev(d2, m2, "eeeeeeee-0000-4000-8000-000000000003", "SessionStart")
	at := t0.Add(20 * time.Minute) // past MachineSilent for both
	got, err := f.st.MachineList(f.ctx, store.MachineQuery{}, at)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		live   int
		silent bool
	}{m1: {2, true}, m2: {1, false}}
	if len(got) != 2 {
		t.Fatalf("%d machines", len(got))
	}
	for _, m := range got {
		w := want[m.ID]
		if m.LiveSessions != w.live || m.Silent != w.silent {
			t.Errorf("%s: live %d silent %v, want live %d silent %v", m.Name, m.LiveSessions, m.Silent, w.live, w.silent)
		}
	}
}

// A session waiting on a permission carries the tool and Claude Code's notification text
// (sessionWaitExpr), so the board can say what it waits on; once it moves on, both are null.
func TestSessionWaitsOnToolAndNotification(t *testing.T) {
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("laptop", m)
	sid := "eeeeeeee-0000-4000-8000-000000000011"
	at := t0
	ev := func(payload string) {
		at = at.Add(time.Second)
		f.now = at
		f.ingest(d, hook(t, m, at, `{"session_id":"`+sid+`","cwd":"/home/me/x",`+payload+`}`))
	}
	ev(`"hook_event_name":"UserPromptSubmit","prompt":"go"`)
	ev(`"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"}`)
	ev(`"hook_event_name":"Notification","message":"Claude needs your permission to use Bash"`)
	v := f.session(sid)
	if v.State != "needs_permission" {
		t.Fatalf("state %s", v.State)
	}
	if v.ToolName == nil || *v.ToolName != "Bash" || v.Notification == nil || *v.Notification != "Claude needs your permission to use Bash" {
		t.Fatalf("tool %v notification %v", v.ToolName, v.Notification)
	}
	ev(`"hook_event_name":"UserPromptSubmit","prompt":"ok"`)
	v = f.session(sid)
	if v.ToolName != nil || v.Notification != nil {
		t.Fatalf("after answer: tool %v notification %v", v.ToolName, v.Notification)
	}
}
