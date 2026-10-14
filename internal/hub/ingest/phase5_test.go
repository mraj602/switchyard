package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/record"
)

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	ing *ingest.Ingester
	now time.Time
	mu  sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), now: t0}
	st, err := store.Open(f.ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: f.clock, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f.st = st
	f.ing = &ingest.Ingester{Store: st, Now: f.clock}
	return f
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) device(name, machine string) store.Device {
	f.t.Helper()
	c, err := auth.Create(f.ctx, f.st, auth.CreateRequest{Kind: "machine", Name: name, Machine: machine})
	if err != nil {
		f.t.Fatal(err)
	}
	return c.Device
}

func body(t *testing.T, recs ...*record.Record) []byte {
	t.Helper()
	var raws []json.RawMessage
	for _, r := range recs {
		b, err := r.Encode()
		if err != nil {
			t.Fatal(err)
		}
		raws = append(raws, b)
	}
	b, _ := json.Marshal(map[string]any{"v": 1, "client": "sy/0.0.0-test", "records": raws})
	return b
}

func info(t *testing.T, machine string, at time.Time, reach string, held int64) *record.Record {
	t.Helper()
	oldest := at.Add(-time.Hour).Format(time.RFC3339Nano)
	r, err := record.NewMachineInfo(record.Meta{Machine: machine, Client: "sy/0.5.0", Now: at}, record.MachineInfoPayload{
		OS: "linux", Arch: "amd64", Sy: "0.5.0", RecordV: 1, Reach: reach,
		Outbox:     record.OutboxHealth{Held: held, OldestUnflushed: &oldest},
		DirectPush: record.DirectPushState{Enabled: true, ServerHost: "ntfy.sh", Private: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Same-machine dedupe (record.md 5): a record id already stored from another device of the
// same machine is a duplicate; from another machine it is id_conflict.
func TestSameMachineDedupe(t *testing.T) {
	f := newFixture(t)
	m1, m2 := "m_"+record.NewID(t0), "m_"+record.NewID(t0.Add(time.Second))
	a := f.device("gpu-box", m1)
	b := f.device("gpu-box (re-paired)", m1)
	c := f.device("laptop", m2)
	r := info(t, m1, t0, record.ReachDirect, 0)
	rep, err := f.ing.Ingest(f.ctx, a, body(t, r), nil)
	if err != nil || len(rep.Accepted) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if rep.Hub == nil || rep.Hub.RecordVMax != record.MaxVersion || rep.Hub.Version == "" {
		t.Fatalf("reply.hub: %+v", rep.Hub)
	}
	rep, err = f.ing.Ingest(f.ctx, b, body(t, r), nil)
	if err != nil || len(rep.Duplicates) != 1 || len(rep.Rejected) != 0 {
		t.Fatalf("same machine, other device: %+v %v", rep, err)
	}
	// A record of machine m2 that reuses the id: another machine's id.
	r2 := info(t, m2, t0, record.ReachDirect, 0)
	r2.ID = r.ID
	rep, err = f.ing.Ingest(f.ctx, c, body(t, r2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rejected) != 1 || rep.Rejected[0].Reason != ingest.IDConflict {
		t.Fatalf("other machine: %+v", rep)
	}
}

// machine_info: only a newer info_at wins; it fills the read model (machines.md 6.1).
func TestMachineInfoNewestWins(t *testing.T) {
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("gpu-box", m)
	newer := info(t, m, t0.Add(time.Minute), record.ReachForward, 37)
	older := info(t, m, t0, record.ReachDirect, 0)
	for _, r := range []*record.Record{newer, older} {
		if rep, err := f.ing.IngestWith(f.ctx, d, body(t, r), ingest.Options{}); err != nil || len(rep.Accepted) != 1 {
			t.Fatalf("%+v %v", rep, err)
		}
	}
	v, err := f.st.MachineByID(f.ctx, m, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if v.Reach == nil || *v.Reach != "forward" || v.Held != 37 || !v.DirectPush || v.OS == nil || *v.OS != "linux" ||
		v.RecordV == nil || *v.RecordV != 1 || int64(v.BacklogOldest) != t0.Add(-time.Hour+time.Minute).UnixMilli() {
		t.Fatalf("%+v", v)
	}
}

func TestVersionStatus(t *testing.T) {
	for _, c := range []struct{ m, h, want string }{
		{"0.5.0", "0.5.0", store.VersionSame},
		{"v0.5.0", "0.5.0+abc", store.VersionSame},
		{"0.4.2", "0.5.0", store.VersionOlder},
		{"0.10.0", "0.9.9", store.VersionNewer},
		{"0.5.0-rc1", "0.5.0", store.VersionUnknown},
		{"dev", "0.5.0", store.VersionUnknown},
		{"0.5.0", "dev", store.VersionUnknown},
		{"", "0.5.0", store.VersionUnknown},
	} {
		if got := store.VersionStatus(c.m, c.h); got != c.want {
			t.Errorf("VersionStatus(%q, %q) = %q, want %q", c.m, c.h, got, c.want)
		}
	}
}

// Remove revokes the machine's devices, hides it from the default list, and a later record
// from it restores it.
func TestMachineRemoveAndRename(t *testing.T) {
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("gpu-box", m)
	if _, err := f.ing.IngestWith(f.ctx, d, body(t, info(t, m, t0, record.ReachDirect, 0)), ingest.Options{}); err != nil {
		t.Fatal(err)
	}
	err := f.st.Write(f.ctx, func(tx *store.Tx) error { return tx.MachineRename(m, "big-box") })
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Write(f.ctx, func(tx *store.Tx) error { return tx.MachineRename("m_nope", "x") }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rename unknown: %v", err)
	}
	var rm store.MachineRemoval
	if err := f.st.Write(f.ctx, func(tx *store.Tx) error {
		var err error
		rm, err = tx.MachineRemove(m, t0.UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(rm.Devices) != 1 || rm.Devices[0] != d.ID {
		t.Fatalf("%+v", rm)
	}
	got, err := f.st.MachineList(f.ctx, store.MachineQuery{}, t0)
	if err != nil || len(got) != 0 {
		t.Fatalf("default list shows a removed machine: %+v %v", got, err)
	}
	got, err = f.st.MachineList(f.ctx, store.MachineQuery{All: true}, t0)
	if err != nil || len(got) != 1 || got[0].Name != "big-box" || got[0].RemovedAt == 0 {
		t.Fatalf("all=true: %+v %v", got, err)
	}
	// Idempotent.
	if err := f.st.Write(f.ctx, func(tx *store.Tx) error { _, err := tx.MachineRemove(m, t0.UnixMilli()); return err }); err != nil {
		t.Fatal(err)
	}
	// A new device of the machine (re-paired) sends again: the machine comes back.
	d2 := f.device("gpu-box", m)
	if rep, err := f.ing.IngestWith(f.ctx, d2, body(t, info(t, m, t0.Add(time.Minute), record.ReachDirect, 0)), ingest.Options{}); err != nil || len(rep.Accepted) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if got, _ := f.st.MachineList(f.ctx, store.MachineQuery{}, t0); len(got) != 1 {
		t.Fatalf("not restored: %+v", got)
	}
}

func freeze(f *fixture, snapshot func(tx *store.Tx) error) error {
	return f.st.Write(f.ctx, func(tx *store.Tx) error {
		if err := tx.PutAlertSetting(store.KeyHubFrozen, []byte(`{"since":1,"moved_to":null}`), 1); err != nil {
			return err
		}
		if snapshot != nil {
			return snapshot(tx)
		}
		return nil
	})
}

func TestFrozenIngest(t *testing.T) {
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("gpu-box", m)
	if err := freeze(f, nil); err != nil {
		t.Fatal(err)
	}
	_, err := f.ing.IngestWith(f.ctx, d, body(t, info(t, m, t0, record.ReachDirect, 0)), ingest.Options{})
	if !errors.Is(err, ingest.ErrFrozen) {
		t.Fatalf("got %v, want ErrFrozen", err)
	}
}

// Under concurrent ingest, no batch is accepted after the freeze commits: every accepted id
// is in the snapshot taken inside the freeze transaction (the export's data).
func TestFrozenUnderConcurrentIngest(t *testing.T) {
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("gpu-box", m)
	var (
		mu       sync.Mutex
		accepted []string
		wg       sync.WaitGroup
	)
	stop := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				at := t0.Add(time.Duration(w*100000+i) * time.Millisecond)
				rep, err := f.ing.IngestWith(f.ctx, d, body(t, info(t, m, at, record.ReachDirect, 0)), ingest.Options{})
				if errors.Is(err, ingest.ErrFrozen) {
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				accepted = append(accepted, rep.Accepted...)
				mu.Unlock()
			}
		}(w)
	}
	time.Sleep(50 * time.Millisecond)
	snap := map[string]bool{}
	if err := freeze(f, func(tx *store.Tx) error {
		rows, err := tx.QueryContext(f.ctx, `SELECT id FROM record`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			snap[id] = true
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	if len(snap) == 0 {
		t.Fatal("no records ingested before the freeze")
	}
	for _, id := range accepted {
		if !snap[id] {
			t.Fatalf("record %s accepted after the freeze committed", id)
		}
	}
	var n int
	if err := f.st.R().QueryRowContext(f.ctx, `SELECT count(*) FROM record`).Scan(&n); err != nil || n != len(snap) {
		t.Fatalf("records after freeze: %d vs snapshot %d (%v)", n, len(snap), err)
	}
}

// Contact (a pull hello) counts as hearing from the machine and sets reach.
func TestContact(t *testing.T) {
	f := newFixture(t)
	m := "m_" + record.NewID(t0)
	d := f.device("gpu-box", m)
	var got ingest.Changes
	f.ing.OnChange = func(ch ingest.Changes) { got = ch }
	n := int64(12)
	if err := f.ing.Contact(f.ctx, d, ingest.Contact{Backlog: &n, BacklogOldest: t0.Add(-time.Minute).UnixMilli(), Reach: "pull", SyVersion: "0.4.0"}); err != nil {
		t.Fatal(err)
	}
	if !got.Machines[m] {
		t.Fatalf("changes: %+v", got)
	}
	v, err := f.st.MachineByID(f.ctx, m, t0)
	if err != nil {
		t.Fatal(err)
	}
	if v.Reach == nil || *v.Reach != "pull" || v.Backlog == nil || *v.Backlog != 12 || v.VersionStatus != store.VersionOlder && v.VersionStatus != store.VersionUnknown {
		t.Fatalf("%+v", v)
	}
	if err := f.ing.Contact(f.ctx, store.Device{ID: "d_x"}, ingest.Contact{}); err == nil {
		t.Fatal("contact for a device without a machine")
	}
}
