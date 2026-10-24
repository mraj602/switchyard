package ingest_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/ingest"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

func sessionList(t *testing.T, machine string, at time.Time, items ...map[string]any) *record.Record {
	t.Helper()
	p := record.SessionListPayload{Complete: true, Source: "claude_agents"}
	for _, it := range items {
		b, _ := json.Marshal(it)
		p.Sessions = append(p.Sessions, b)
	}
	r, err := record.NewSessionList(record.Meta{Machine: machine, Slot: "~/.claude", Client: "wy/0.5.0", Now: at}, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A session first seen idle by a session_list (wy set up on a
// machine with old Claude processes still running) is done since its last transcript row,
// not since the list ran, whichever of the list and the transcript import the hub gets
// first; so an idle process from last week is dormant, not open, although it is listed.
// wy hub refold repairs rows a hub without this rule left.
func TestListedIdleSessionIsDoneSinceItsLastActivity(t *testing.T) {
	const (
		idle    = "eeeeeeee-0000-4000-8000-000000000001" // idle for a week, rows imported after the list
		bare    = "eeeeeeee-0000-4000-8000-000000000002" // idle, no transcript: since its start
		busy    = "eeeeeeee-0000-4000-8000-000000000003" // working now
		earlier = "eeeeeeee-0000-4000-8000-000000000004" // rows imported before the first list
	)
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("laptop", m)
	day := 24 * time.Hour
	lastRow, start := t0.Add(-7*day), t0.Add(-20*day)
	f.now = t0
	f.ingest(d, tokenRows(t, m, t0.Add(-time.Second), []record.TokenRow{
		row("msg_e1", earlier, t0.Add(-3*day), "~/x"), row("msg_e2", earlier, t0.Add(-2*day), "~/x"),
	}, earlier))
	ms := func(t time.Time) int64 { return t.UnixMilli() }
	f.ingest(d, sessionList(t, m, t0,
		map[string]any{"sessionId": idle, "status": "idle", "cwd": "/home/me/x", "startedAt": ms(start)},
		map[string]any{"sessionId": bare, "status": "idle", "cwd": "/home/me/y", "startedAt": ms(t0.Add(-30 * time.Hour))},
		map[string]any{"sessionId": busy, "status": "busy", "cwd": "/home/me/z", "startedAt": ms(t0.Add(-time.Hour))},
		map[string]any{"sessionId": earlier, "status": "idle", "cwd": "/home/me/x"},
	))
	f.ingest(d, tokenRows(t, m, t0.Add(time.Second), []record.TokenRow{
		row("msg_1", idle, start, "~/x"), row("msg_2", idle, lastRow, "~/x"),
	}, idle))

	check := func(when string) {
		t.Helper()
		for sid, want := range map[string]time.Time{idle: lastRow, bare: t0.Add(-30 * time.Hour), busy: t0, earlier: t0.Add(-2 * day)} {
			v := f.session(sid)
			if int64(v.StateSince) != ms(want) {
				t.Errorf("%s: %s since %s, want %s", when, sid[len(sid)-1:], time.UnixMilli(int64(v.StateSince)).UTC(), want)
			}
			if v.Dormant != (sid != busy) {
				t.Errorf("%s: %s (%s) dormant %v", when, sid[len(sid)-1:], v.State, v.Dormant)
			}
		}
		if c, err := f.st.Counts(f.ctx, t0); err != nil || c.Live != 1 || c.Working != 1 || c.Done != 0 {
			t.Errorf("%s: counts %+v %v", when, c, err)
		}
		// Still in the session list's days window: the processes run, so they can be found.
		if list, _, err := f.st.Sessions(f.ctx, store.SessionFilter{}, t0); err != nil || len(list) != 4 {
			t.Errorf("%s: list has %d sessions (%v)", when, len(list), err)
		}
	}
	check("ingest")

	// What a hub without the rule left: done since the list ran.
	err := f.st.Write(f.ctx, func(tx *store.Tx) error {
		_, err := tx.ExecContext(f.ctx, `UPDATE session_state SET since_at = ? WHERE state = 'done'`, ms(t0))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := f.st.Counts(f.ctx, t0); c.Live != 4 {
		t.Fatalf("broken state not reproduced: %+v", c)
	}
	rep, err := ingest.Refold(f.ctx, f.st, false)
	if err != nil || rep.Changed != 3 || rep.Live != 4 {
		t.Fatalf("refold %+v %v", rep, err)
	}
	check("refold")
	if again, err := ingest.Refold(f.ctx, f.st, false); err != nil || again.Changed != 0 {
		t.Fatalf("second refold %+v %v", again, err)
	}
}
