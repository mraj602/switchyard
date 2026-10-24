package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openPullStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const pullMachine = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"

func TestPullTargetLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openPullStore(t)
	tg, err := s.AddPullTarget(ctx, PullTarget{Name: "gpu", MachineID: pullMachine, Host: "h", User: "u", HostKeys: []string{"ssh-ed25519 AAAA"}})
	if err != nil {
		t.Fatal(err)
	}
	if tg.State != PullStateNew || tg.Port != 22 || tg.IntervalS != 60 || tg.RemoteSy != ".local/bin/wy" || tg.DeviceID == "" {
		t.Fatalf("%+v", tg)
	}
	// The pull device: kind machine, bound to the machine, created_by pull:<id>, scope write.
	d, err := s.DeviceByID(ctx, tg.DeviceID)
	if err != nil || d.Kind != "machine" || d.MachineID != pullMachine || d.CreatedBy != "pull:"+tg.ID || len(d.Scopes) != 1 || d.Scopes[0] != "write" {
		t.Fatalf("%+v %v", d, err)
	}
	// Its token hash is random and unusable: no token maps to it.
	if _, err := s.DeviceByHash(ctx, make([]byte, 32)); !errors.Is(err, ErrNotFound) {
		t.Fatal("a zero hash found a device")
	}
	if _, err := s.AddPullTarget(ctx, PullTarget{Name: "gpu", Host: "h2", User: "u"}); !errors.Is(err, ErrPullExists) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := s.AddPullTarget(ctx, PullTarget{Name: "other", MachineID: pullMachine, Host: "h2", User: "u"}); !errors.Is(err, ErrPullExists) {
		t.Fatalf("duplicate machine: %v", err)
	}
	if got, err := s.PullTargetByMachine(ctx, pullMachine); err != nil || got.ID != tg.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := s.PullTargetByName(ctx, "gpu"); err != nil || got.ID != tg.ID {
		t.Fatal(got, err)
	}
	if _, err := s.PullTargetByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}

	// Runs.
	now := s.Now().UnixMilli()
	if err := s.RecordPullRun(ctx, tg.ID, PullResult{State: PullStateFailing, Error: "refused", FailCount: 2, NextAt: now + 1000}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.PullTargetByID(ctx, tg.ID)
	if got.State != PullStateFailing || got.FailCount != 2 || got.LastError != "refused" || got.LastTryAt == 0 || got.LastOKAt != 0 || got.NextAt != now+1000 {
		t.Fatalf("%+v", got)
	}
	if due, _ := s.DuePullTargets(ctx, now); len(due) != 0 {
		t.Fatal("not due yet")
	}
	if due, _ := s.DuePullTargets(ctx, now+1000); len(due) != 1 {
		t.Fatal("due")
	}
	if err := s.RecordPullRun(ctx, tg.ID, PullResult{State: PullStateOK, OK: true, FailCount: 9}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PullTargetByID(ctx, tg.ID)
	if got.State != PullStateOK || got.FailCount != 0 || got.LastError != "" || got.LastOKAt == 0 || got.NextAt != now+1000 {
		t.Fatalf("%+v", got)
	}
	// host_key and refused wait for the owner: never due.
	_ = s.RecordPullRun(ctx, tg.ID, PullResult{State: PullStateHostKey, Error: "changed"})
	if due, _ := s.DuePullTargets(ctx, now+1e9); len(due) != 0 {
		t.Fatal("host_key target is due")
	}
	if err := s.TrustPullKeys(ctx, tg.ID, []string{"ssh-ed25519 BBBB"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PullTargetByID(ctx, tg.ID)
	if got.State != PullStateNew || got.FailCount != 0 || len(got.HostKeys) != 1 || got.HostKeys[0] != "ssh-ed25519 BBBB" {
		t.Fatalf("%+v", got)
	}
	// disable / enable.
	_ = s.SetPullEnabled(ctx, tg.ID, false)
	got, _ = s.PullTargetByID(ctx, tg.ID)
	if got.Enabled || got.State != PullStateDisabled {
		t.Fatalf("%+v", got)
	}
	_ = s.RecordPullRun(ctx, tg.ID, PullResult{State: PullStateOK, OK: true})
	if got, _ = s.PullTargetByID(ctx, tg.ID); got.State != PullStateDisabled {
		t.Fatalf("a run result must not re-enable a disabled target: %+v", got)
	}
	_ = s.SetPullEnabled(ctx, tg.ID, true)
	if got, _ = s.PullTargetByID(ctx, tg.ID); !got.Enabled || got.State != PullStateNew {
		t.Fatalf("%+v", got)
	}
	// remove --revoke revokes the device.
	if err := s.RemovePullTarget(ctx, tg.ID, true); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.DeviceByID(ctx, tg.DeviceID); d.RevokedAt == 0 {
		t.Fatal("device not revoked")
	}
	if err := s.RemovePullTarget(ctx, tg.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestBindPullTargetAndHeartbeat(t *testing.T) {
	ctx := context.Background()
	s := openPullStore(t)
	tg, err := s.AddPullTarget(ctx, PullTarget{Name: "late", Host: "h", User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := s.BindPullTarget(ctx, tg.ID, pullMachine, "gpu-box")
	if err != nil || dev == "" {
		t.Fatal(dev, err)
	}
	again, err := s.BindPullTarget(ctx, tg.ID, pullMachine, "gpu-box")
	if err != nil || again != dev {
		t.Fatalf("rebinding the same machine must be a no-op: %q %v", again, err)
	}
	if _, err := s.BindPullTarget(ctx, tg.ID, "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4Z", "x"); err == nil {
		t.Fatal("bound to a second machine")
	}
	if err := s.PullHeartbeat(ctx, PullHello{Machine: pullMachine, SyVersion: "0.5.0", Backlog: 7, BacklogOldest: 1234, RecordV: 1}); err != nil {
		t.Fatal(err)
	}
	var reach string
	var backlog, oldest, recordV int64
	if err := s.R().QueryRow(`SELECT reach, backlog, coalesce(backlog_oldest, 0), record_v FROM machine WHERE id = ?`, pullMachine).Scan(&reach, &backlog, &oldest, &recordV); err != nil {
		t.Fatal(err)
	}
	if reach != "pull" || backlog != 7 || oldest != 1234 || recordV != 1 {
		t.Fatal(reach, backlog, oldest, recordV)
	}
	// A drained outbox clears the oldest age.
	_ = s.PullHeartbeat(ctx, PullHello{Machine: pullMachine, Backlog: 0})
	_ = s.R().QueryRow(`SELECT coalesce(backlog_oldest, 0) FROM machine WHERE id = ?`, pullMachine).Scan(&oldest)
	if oldest != 0 {
		t.Fatalf("backlog_oldest %d not cleared", oldest)
	}
	if err := s.SetMachineHeld(ctx, pullMachine, 4); err != nil {
		t.Fatal(err)
	}
}
