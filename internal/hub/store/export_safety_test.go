package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/hublock"
)

// The plaintext snapshot never lands next to --out.
func TestExportSnapshotIsPrivateInDataDir(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	s := openTest(t, dataDir)
	seedDevice(t, s)
	outDir := filepath.Join(t.TempDir(), "shared")
	var work string
	exportWorkHook = func(w string) {
		work = w
		// while the snapshot exists, the out folder holds no work folder of ours
		ents, _ := os.ReadDir(outDir)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".export-") {
				t.Errorf("snapshot folder %s next to --out", e.Name())
			}
		}
	}
	t.Cleanup(func() { exportWorkHook = nil })
	out := filepath.Join(outDir, "hub.syx")
	if _, err := s.Export(ctx, ExportOptions{Out: out, StateDir: state(t), Passphrase: "pw", SyVersion: "t"}); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(work) != dataDir {
		t.Fatalf("snapshot in %s, want a folder of %s", work, dataDir)
	}
	if _, err := os.Stat(work); err == nil {
		t.Fatal("snapshot folder left behind")
	}
	ents, _ := os.ReadDir(outDir)
	if len(ents) != 1 || ents[0].Name() != "hub.syx" {
		t.Fatalf("out folder holds %v", ents)
	}
}

// A missing pair_pepper fails the export (and unfreezes again).
func TestExportFailsWithoutPepperAndUnfreezes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, t.TempDir())
	st := t.TempDir() // no pair_pepper
	_, err := s.Export(ctx, ExportOptions{Out: filepath.Join(t.TempDir(), "x.syx"), StateDir: st, SyVersion: "t"})
	if err == nil || !strings.Contains(err.Error(), "pair_pepper") {
		t.Fatalf("err = %v", err)
	}
	if fr, _ := s.Frozen(ctx); fr != nil {
		t.Fatal("a failed export left the hub frozen")
	}
}

// A cancelled export (Ctrl-C) unfreezes the hub it froze and leaves no snapshot.
func TestExportCancelledUnfreezesAndCleansUp(t *testing.T) {
	data := t.TempDir()
	s := openTest(t, data)
	ctx, cancel := context.WithCancel(context.Background())
	var work string
	exportWorkHook = func(w string) { work = w; cancel() }
	t.Cleanup(func() { exportWorkHook = nil })
	_, err := s.Export(ctx, ExportOptions{Out: filepath.Join(t.TempDir(), "x.syx"), StateDir: state(t), SyVersion: "t"})
	if err == nil {
		t.Fatal("export of a cancelled context succeeded")
	}
	if fr, _ := s.Frozen(context.Background()); fr != nil {
		t.Fatal("hub left frozen")
	}
	if _, serr := os.Stat(work); serr == nil {
		t.Fatal("snapshot left on disk")
	}
}

// The pull key comes from PullDir, not <state>/pull.
func TestExportImportHonourPullDir(t *testing.T) {
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	sd := t.TempDir()
	os.WriteFile(filepath.Join(sd, "pair_pepper"), []byte("pp"), 0o600)
	os.WriteFile(filepath.Join(sd, "hub_sign.key"), []byte("hk"), 0o600)
	pd := t.TempDir()
	os.WriteFile(filepath.Join(pd, "id_ed25519"), []byte("KEY"), 0o600)
	out := filepath.Join(t.TempDir(), "h.syx")
	if _, err := src.Export(ctx, ExportOptions{Out: out, StateDir: sd, PullDir: pd, NoFreeze: true, SyVersion: "t"}); err != nil {
		t.Fatal(err)
	}
	dstData, dstState, dstPull := t.TempDir(), t.TempDir(), t.TempDir()
	if _, err := Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(dstData, "hub.db"), StateDir: dstState, PullDir: dstPull}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dstPull, "id_ed25519")); string(b) != "KEY" {
		t.Fatalf("pull key not in the pull dir: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dstState, "pull")); err == nil {
		t.Fatal("pull key written under <state>/pull although PullDir was given")
	}
}

// A running `wy hub serve` (hub.lock) stops an import.
func TestImportRefusesWhileHubLockHeld(t *testing.T) {
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	out := filepath.Join(t.TempDir(), "h.syx")
	if _, err := src.Export(ctx, ExportOptions{Out: out, StateDir: state(t), NoFreeze: true, SyVersion: "t"}); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	l, err := hublock.Acquire(data)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(data, "hub.db"), StateDir: t.TempDir()})
	if !errors.Is(err, ErrHubRunning) {
		t.Fatalf("err = %v", err)
	}
	l.Release()
	if _, err = Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(data, "hub.db"), StateDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

// An import that fails while swapping state files leaves the hub as it was.
func TestImportRollsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	seedDevice(t, src)
	out := filepath.Join(t.TempDir(), "h.syx")
	if _, err := src.Export(ctx, ExportOptions{Out: out, StateDir: state(t), NoFreeze: true, SyVersion: "t"}); err != nil {
		t.Fatal(err)
	}
	data, sd := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(sd, "pair_pepper"), []byte("OLD"), 0o600)
	// An existing empty hub database, so the swap has something to restore.
	empty := openTest(t, data)
	empty.Close()
	before, _ := os.ReadFile(filepath.Join(data, "hub.db"))
	// The pull dir's parent is a regular file: creating it fails after the pepper was swapped.
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0o600)
	_, err := Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(data, "hub.db"), StateDir: sd,
		PullDir: filepath.Join(blocker, "pull")})
	if err == nil {
		t.Fatal("import succeeded")
	}
	if b, _ := os.ReadFile(filepath.Join(sd, "pair_pepper")); string(b) != "OLD" {
		t.Fatalf("pepper not restored: %q", b)
	}
	after, rerr := os.ReadFile(filepath.Join(data, "hub.db"))
	if rerr != nil || len(after) != len(before) {
		t.Fatalf("database not restored: %v (%d vs %d bytes)", rerr, len(after), len(before))
	}
	ents, _ := os.ReadDir(sd)
	for _, e := range ents {
		if e.Name() != "pair_pepper" {
			t.Errorf("leftover %s in state dir", e.Name())
		}
	}
	// and the hub is still importable afterwards
	if _, err := Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(data, "hub.db"), StateDir: sd}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// While frozen, non-ingest writes are refused with ErrFrozen.
func TestFrozenRefusesOtherWrites(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, t.TempDir())
	seedDevice(t, s)
	tg, err := s.AddPullTarget(ctx, PullTarget{Name: "box", Host: "h", User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Freeze(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindPullTarget(ctx, tg.ID, "m_9", "n"); !errors.Is(err, ErrFrozen) {
		t.Errorf("BindPullTarget: %v", err)
	}
	if err := s.RecordPullRun(ctx, tg.ID, PullResult{State: PullStateOK, OK: true}); !errors.Is(err, ErrFrozen) {
		t.Errorf("RecordPullRun: %v", err)
	}
	if err := s.SetMachineHeld(ctx, "m_1", 3); !errors.Is(err, ErrFrozen) {
		t.Errorf("SetMachineHeld: %v", err)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		return tx.InsertDevice(Device{ID: "dev_2", Kind: "machine", Name: "x", CreatedBy: "cli"}, []byte("h"))
	})
	if !errors.Is(err, ErrFrozen) {
		t.Errorf("InsertDevice: %v", err)
	}
	if _, err := s.Unfreeze(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindPullTarget(ctx, tg.ID, "m_9", "n"); err != nil {
		t.Errorf("after unfreeze: %v", err)
	}
}

// A "Pull now" that lands during a run survives the run's own next time.
func TestRecordPullRunKeepsPullNowDuringRun(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, t.TempDir())
	tg, err := s.AddPullTarget(ctx, PullTarget{Name: "box", Host: "h", User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	startNext := tg.NextAt
	time.Sleep(3 * time.Millisecond)
	if err := s.SchedulePullNow(ctx, tg.ID); err != nil { // the "Pull now" during the run
		t.Fatal(err)
	}
	pulled, _ := s.PullTargetByID(ctx, tg.ID)
	far := time.Now().Add(time.Hour).UnixMilli()
	err = s.RecordPullRun(ctx, tg.ID, PullResult{State: PullStateOK, OK: true, NextAt: far, GuardNextAt: true, WasNextAt: startNext})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.PullTargetByID(ctx, tg.ID)
	if got.NextAt != pulled.NextAt {
		t.Fatalf("next_at = %d, want the pull-now value %d", got.NextAt, pulled.NextAt)
	}
	// without a pull-now in between, the run's time applies
	err = s.RecordPullRun(ctx, tg.ID, PullResult{State: PullStateOK, OK: true, NextAt: far, GuardNextAt: true, WasNextAt: got.NextAt})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ = s.PullTargetByID(ctx, tg.ID); got.NextAt != far {
		t.Fatalf("next_at = %d, want %d", got.NextAt, far)
	}
}

func TestMachineRecentDirect(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, t.TempDir())
	seedDevice(t, s)
	if d, err := s.MachineRecentDirect(ctx, "m_1", time.Minute); err != nil || d {
		t.Fatalf("never heard: %v %v", d, err)
	}
	s.Write(ctx, func(tx *Tx) error {
		_, err := tx.exec(`UPDATE machine SET last_seen = ? WHERE id = 'm_1'`, s.Now().UnixMilli())
		return err
	})
	if d, _ := s.MachineRecentDirect(ctx, "m_1", time.Minute); !d {
		t.Fatal("recent upload not seen as direct")
	}
	s.Write(ctx, func(tx *Tx) error {
		_, err := tx.exec(`UPDATE machine SET reach = 'pull' WHERE id = 'm_1'`)
		return err
	})
	if d, _ := s.MachineRecentDirect(ctx, "m_1", time.Minute); d {
		t.Fatal("a pulled machine is not direct")
	}
}
