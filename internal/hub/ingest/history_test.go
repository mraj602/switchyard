package ingest_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/record"
)

const (
	sidA = "aaaaaaaa-0000-4000-8000-000000000001" // rows and facts
	sidB = "bbbbbbbb-0000-4000-8000-000000000002" // facts only, no rows
	sidC = "cccccccc-0000-4000-8000-000000000003" // live through hooks
)

func tokenRows(t *testing.T, machine string, at time.Time, rows []record.TokenRow, facts ...string) *record.Record {
	t.Helper()
	p := record.TokenRowsPayload{Rows: rows}
	for _, id := range facts {
		b, _ := json.Marshal(map[string]any{"session_id": id, "title": "t " + id[:4]})
		p.Sessions = append(p.Sessions, b)
	}
	r, err := record.NewTokenRows(record.Meta{Machine: machine, Slot: "~/.claude", Client: "sy/0.5.0", Now: at}, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func row(key, sid string, at time.Time, cwd string) record.TokenRow {
	return record.TokenRow{Key: key, SessionID: sid, TS: at.UTC().Format(time.RFC3339Nano), Cwd: cwd, Input: 1, Output: 2}
}

func hook(t *testing.T, machine string, at time.Time, payload string) *record.Record {
	t.Helper()
	r, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: "~/.claude", Client: "sy/0.5.0", Now: at}, []byte(payload), record.HookOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *fixture) session(id string) store.SessionView {
	f.t.Helper()
	v, err := f.st.Session(f.ctx, id, f.clock())
	if err != nil {
		f.t.Fatalf("session %s: %v", id, err)
	}
	return v
}

func (f *fixture) ingest(d store.Device, recs ...*record.Record) {
	f.t.Helper()
	rep, err := f.ing.Ingest(f.ctx, d, body(f.t, recs...), nil)
	if err != nil || len(rep.Rejected) > 0 {
		f.t.Fatalf("%+v %v", rep, err)
	}
}

// Sessions made from transcript history (token_rows) are ended history sessions with their
// real times and project, not live "done" sessions stamped with the scan time (states.md 5.1).
func TestTranscriptSessionsAreHistory(t *testing.T) {
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("laptop", m)
	first, last := t0.Add(-30*24*time.Hour), t0.Add(-29*24*time.Hour)
	f.ingest(d, tokenRows(t, m, t0, []record.TokenRow{
		row("msg_2", sidA, last, "~/work/api"), row("msg_1", sidA, first, "~/work/api"),
	}, sidA, sidB))

	a := f.session(sidA)
	if a.State != "ended" || a.Detail == nil || *a.Detail != "history" || a.NeedsYou {
		t.Fatalf("A: %s %v", a.State, a.Detail)
	}
	if int64(a.StartedAt) != first.UnixMilli() || int64(a.EndedAt) != last.UnixMilli() || int64(a.StateSince) != last.UnixMilli() {
		t.Fatalf("A times: started %d ended %d since %d", a.StartedAt, a.EndedAt, a.StateSince)
	}
	if a.Project == nil || *a.Project != "api" {
		t.Fatalf("A project %v", a.Project)
	}
	b := f.session(sidB)
	if b.State != "ended" || b.StartedAt != 0 || b.EndedAt != 0 || b.StateSince != 0 || b.Activity != 0 {
		t.Fatalf("B: %+v", b)
	}
	c, err := f.st.Counts(f.ctx, t0)
	if err != nil || c.Live != 0 || c.Done != 0 {
		t.Fatalf("counts %+v %v", c, err)
	}
	list, _, err := f.st.Sessions(f.ctx, store.SessionFilter{}, t0)
	if err != nil || len(list) != 0 {
		t.Fatalf("default list shows history: %d %v", len(list), err)
	}

	// Newer transcript activity moves ended_at; the start stays the earliest row.
	later := t0.Add(-time.Hour)
	f.ingest(d, tokenRows(t, m, t0.Add(time.Minute), []record.TokenRow{row("msg_3", sidA, later, "~/work/api")}))
	if a = f.session(sidA); int64(a.EndedAt) != later.UnixMilli() || int64(a.StartedAt) != first.UnixMilli() {
		t.Fatalf("A after newer rows: ended %d started %d", a.EndedAt, a.StartedAt)
	}

	// A hook event brings it back as a live session.
	f.now = t0.Add(2 * time.Minute)
	f.ingest(d, hook(t, m, f.now, `{"hook_event_name":"UserPromptSubmit","session_id":"`+sidA+`","cwd":"/home/me/work/api"}`))
	if a = f.session(sidA); a.State != "working" || a.EndedAt != 0 {
		t.Fatalf("A after a hook: %s ended %d", a.State, a.EndedAt)
	}
	if c, _ = f.st.Counts(f.ctx, f.now); c.Live != 1 || c.Working != 1 {
		t.Fatalf("counts %+v", c)
	}

	// A session live through hooks keeps its state when its transcript is scanned.
	f.ingest(d, hook(t, m, f.now, `{"hook_event_name":"SessionStart","source":"startup","session_id":"`+sidC+`","cwd":"/home/me/x"}`))
	f.ingest(d, tokenRows(t, m, f.now.Add(time.Second), []record.TokenRow{row("msg_c", sidC, f.now.Add(-time.Hour), "~/x")}, sidC))
	if cs := f.session(sidC); cs.State != "done" || cs.Detail == nil || *cs.Detail != "started" || int64(cs.StartedAt) != f.now.Add(-time.Hour).UnixMilli() {
		t.Fatalf("C: %s %v started %d", cs.State, cs.Detail, cs.StartedAt)
	}
}

// sy hub refold repairs sessions typed by an older hub (live "done", started at the scan
// time, no project) from the stored records, and is idempotent.
func TestRefoldRepairsHistory(t *testing.T) {
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("laptop", m)
	first, last := t0.Add(-10*24*time.Hour), t0.Add(-9*24*time.Hour)
	f.ingest(d, tokenRows(t, m, t0, []record.TokenRow{row("msg_1", sidA, first, "~/work/api"), row("msg_2", sidA, last, "")}, sidA, sidB))
	f.ingest(d, hook(t, m, t0, `{"hook_event_name":"SessionStart","source":"startup","session_id":"`+sidC+`","cwd":"/home/me/x"}`))
	// What an older hub left: no fold, started_at = the token_rows record's ts, no project.
	err := f.st.Write(f.ctx, func(tx *store.Tx) error {
		for _, q := range []string{
			`DELETE FROM session_transition WHERE session_id IN ('` + sidA + `', '` + sidB + `')`,
			`DELETE FROM session_state WHERE session_id IN ('` + sidA + `', '` + sidB + `')`,
			`UPDATE session SET ended_at = NULL, end_reason = NULL, project = NULL, started_at = ` + itoa(t0.UnixMilli()) +
				` WHERE id IN ('` + sidA + `', '` + sidB + `')`,
		} {
			if _, err := tx.ExecContext(f.ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := f.st.Counts(f.ctx, t0); c.Live != 3 {
		t.Fatalf("broken state not reproduced: %+v", c)
	}
	dry, err := ingest.Refold(f.ctx, f.st, true)
	if err != nil || dry.Changed != 2 || dry.History != 2 || dry.Live != 1 {
		t.Fatalf("dry %+v %v", dry, err)
	}
	if c, _ := f.st.Counts(f.ctx, t0); c.Live != 3 {
		t.Fatal("dry run wrote")
	}
	rep, err := ingest.Refold(f.ctx, f.st, false)
	if err != nil || rep.Changed != 2 || rep.Started != 2 || rep.Project != 1 || rep.ByState["ended"] != 2 || rep.ByState["done"] != 1 {
		t.Fatalf("refold %+v %v", rep, err)
	}
	a := f.session(sidA)
	if a.State != "ended" || int64(a.StartedAt) != first.UnixMilli() || int64(a.EndedAt) != last.UnixMilli() || *a.Project != "api" {
		t.Fatalf("A: %+v", a)
	}
	if b := f.session(sidB); b.StartedAt != 0 || b.State != "ended" {
		t.Fatalf("B: %+v", b)
	}
	if c := f.session(sidC); c.State != "done" {
		t.Fatalf("C: %+v", c)
	}
	if c, _ := f.st.Counts(f.ctx, t0); c.Live != 1 {
		t.Fatalf("counts after refold %+v", c)
	}
	again, err := ingest.Refold(f.ctx, f.st, false)
	if err != nil || again.Changed != 0 || again.Started != 0 || again.Project != 0 {
		t.Fatalf("second refold %+v %v", again, err)
	}
}

func itoa(v int64) string { b, _ := json.Marshal(v); return string(b) }
