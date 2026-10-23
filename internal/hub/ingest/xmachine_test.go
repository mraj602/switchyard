package ingest_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/record"
)

// record.md 5.1: one machine's token cannot change another machine's sessions.

const (
	xmA   = "m_01HZZZZZZZZZZZZZZZZZZZZZZA"
	xmB   = "m_01HZZZZZZZZZZZZZZZZZZZZZZB"
	xmSID = "11111111-2222-3333-4444-555555555555"
)

func xmHook(t *testing.T, machine, payload string, at time.Time) *record.Record {
	t.Helper()
	r, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: "~/.claude", Client: "sy/0.5.0", Now: at}, []byte(payload), record.HookOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// xmStart makes session xmSID on machine A and returns B's device.
func xmStart(t *testing.T, f *fixture) store.Device {
	t.Helper()
	a := f.device("A", xmA)
	rep, err := f.ing.Ingest(f.ctx, a, body(t, xmHook(t, xmA,
		`{"session_id":"`+xmSID+`","hook_event_name":"SessionStart","source":"startup","cwd":"/home/a/proj"}`, t0)), nil)
	if err != nil || len(rep.Accepted) != 1 {
		t.Fatal(err, rep)
	}
	return f.device("B", xmB)
}

func xmCheckUntouched(t *testing.T, f *fixture) store.SessionView {
	t.Helper()
	v, err := f.st.Session(f.ctx, xmSID, t0.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if v.Machine == nil || *v.Machine != xmA {
		t.Fatalf("machine = %v, want %s", v.Machine, xmA)
	}
	if v.Cwd == nil || *v.Cwd != "/home/a/proj" {
		t.Fatalf("cwd = %v, want /home/a/proj", v.Cwd)
	}
	if v.Name != nil && *v.Name != "" {
		t.Fatalf("name = %q, want none", *v.Name)
	}
	if v.State == "ended" || v.State == "gone" {
		t.Fatalf("state = %s: machine B ended machine A's session", v.State)
	}
	return v
}

func TestForeignHookEventIsNotApplied(t *testing.T) {
	f := newFixture(t)
	b := xmStart(t, f)
	rep, err := f.ing.Ingest(f.ctx, b, body(t, xmHook(t, xmB,
		`{"session_id":"`+xmSID+`","hook_event_name":"SessionEnd","reason":"logout","cwd":"/evil","session_name":"PWNED"}`,
		t0.Add(time.Second))), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Stored (the record is valid) but not applied: typed = 0 with a type_error.
	if len(rep.Accepted) != 1 {
		t.Fatalf("reply = %+v", rep)
	}
	xmCheckUntouched(t, f)
}

func TestForeignSessionListAndTokenRowsAreSkipped(t *testing.T) {
	f := newFixture(t)
	b := xmStart(t, f)
	at := t0.Add(time.Second)
	m := record.Meta{Machine: xmB, Slot: "~/.claude", Client: "sy/0.5.0", Now: at}
	item, _ := json.Marshal(map[string]any{"sessionId": xmSID, "cwd": "/evil", "name": "PWNED", "status": "idle"})
	sl, err := record.NewSessionList(m, record.SessionListPayload{Complete: true, Source: "test", Sessions: []json.RawMessage{item}})
	if err != nil {
		t.Fatal(err)
	}
	fact, _ := json.Marshal(map[string]any{"session_id": xmSID, "owner_account": "acc", "owner_org": "org", "title": "PWNED"})
	m.Now = at.Add(time.Millisecond)
	tr, err := record.NewTokenRows(m, record.TokenRowsPayload{
		Rows:     []record.TokenRow{{Key: "k1", TS: at.Format(time.RFC3339Nano), SessionID: xmSID, Cwd: "/evil", Input: 1_000_000}},
		Sessions: []json.RawMessage{fact},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ing.Ingest(f.ctx, b, body(t, sl, tr), nil); err != nil {
		t.Fatal(err)
	}
	xmCheckUntouched(t, f)
	err = f.st.Write(f.ctx, func(tx *store.Tx) error {
		last, err := tx.LastTokenRowAt(xmSID, at.Add(time.Hour).UnixMilli())
		if err != nil {
			return err
		}
		if last != 0 {
			t.Errorf("machine B's token row was merged into machine A's session")
		}
		s, _, err := tx.GetSession(xmSID)
		if err != nil {
			return err
		}
		if s.Title != "" {
			t.Errorf("title = %q, want none", s.Title)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOwnMachineStillUpdatesItsSession(t *testing.T) {
	f := newFixture(t)
	xmStart(t, f)
	a := f.device("A2", xmA) // a second device of the same machine (re-pairing)
	if _, err := f.ing.Ingest(f.ctx, a, body(t, xmHook(t, xmA,
		`{"session_id":"`+xmSID+`","hook_event_name":"SessionEnd","reason":"logout","cwd":"/home/a/proj"}`,
		t0.Add(time.Second))), nil); err != nil {
		t.Fatal(err)
	}
	v, err := f.st.Session(f.ctx, xmSID, t0.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if v.State != "ended" {
		t.Fatalf("state = %s, want ended", v.State)
	}
}

func TestWriteWithoutMachineIsRefused(t *testing.T) {
	f := newFixture(t)
	dev := store.Device{ID: "dev_nomachine", Kind: "desktop", Name: "x", Scopes: []string{"write"}}
	_, err := f.ing.Ingest(f.ctx, dev, body(t, xmHook(t, xmA,
		`{"session_id":"`+xmSID+`","hook_event_name":"SessionStart","source":"startup","cwd":"/x"}`, t0)), nil)
	if !errors.Is(err, ingest.ErrNoMachine) {
		t.Fatalf("err = %v, want ErrNoMachine", err)
	}
	if _, err := f.st.Session(f.ctx, xmSID, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session stored from a machine-less device: %v", err)
	}
}
