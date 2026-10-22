package pull_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/drainproto"
	"github.com/mraj602/switchyard/internal/hub/auth"
	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/pull"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// A store error while checking the target is not "refuse disabled".
func TestSessionTransientStoreErrorIsAFailureNotARefusal(t *testing.T) {
	e := newEnv(t)
	e.st.Close() // every read now fails: a transient error, not a missing target
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()
	go func() {
		_ = drainproto.NewWriter(w1).Write(drainproto.Hello{Proto: drainproto.Proto, Machine: machineID, Name: "gpu-box", Sy: "0.5.0", RecordV: 1})
	}()
	go io.Copy(io.Discard, r2)
	sess := &pull.Session{Store: e.st, Ingester: e.ing, HelloTimeout: 2 * time.Second, BatchTimeout: time.Second}
	res := sess.Serve(context.Background(), e.target, r1, w2)
	w1.Close()
	w2.Close()
	if res.Outcome != pull.OutcomeFailed || res.Code != "" || res.Err == nil {
		t.Fatalf("%+v", res)
	}
}

// A hello cannot bind a machine that uploads directly right now.
func TestFirstHelloDoesNotBindADirectMachine(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const direct = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4D"
	c, err := auth.Create(ctx, e.st, auth.CreateRequest{Kind: "machine", Name: "direct-box", Machine: direct})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.ing.Contact(ctx, c.Device, ingest.Contact{Reach: "direct"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RemovePullTarget(ctx, e.target.ID, true); err != nil { // frees machineID
		t.Fatal(err)
	}
	unbound, err := e.st.AddPullTarget(ctx, store.PullTarget{Name: "unbound", Host: "u.example", User: "dev", HostKeys: []string{hostKeyA}})
	if err != nil {
		t.Fatal(err)
	}
	e.target = unbound
	ep := e.session(faults{}, func(_ *pull.Session, o *client.DrainOptions) { o.Machine = direct })
	if ep.hub.Outcome != pull.OutcomeRefused || ep.hub.Code != drainproto.RefuseMachineMismatch || !strings.Contains(ep.hub.Msg, "directly") {
		t.Fatalf("hub %+v", ep.hub)
	}
	if got, _ := e.st.PullTargetByID(ctx, unbound.ID); got.MachineID != "" {
		t.Fatalf("target bound to %s", got.MachineID)
	}
	// A machine nobody has heard from binds as before.
	e.fill(3)
	ep = e.session(faults{}, nil) // machineID, never seen
	if ep.hub.Outcome != pull.OutcomeOK {
		t.Fatalf("hub %+v remote %+v", ep.hub, ep.remote)
	}
	if got, _ := e.st.PullTargetByID(ctx, unbound.ID); got.MachineID != machineID {
		t.Fatalf("not bound: %+v", got)
	}
}

// A "Pull now" that arrives while a run is going is not overwritten by
// the run's own next time.
func TestSchedulerKeepsPullNowMadeDuringARun(t *testing.T) {
	e := newEnv(t)
	var log starterLog
	inner := failStarter(&log, 255, "connection refused")
	starter := func(ctx context.Context, argv []string) (pull.Proc, error) {
		if err := e.st.SchedulePullNow(context.Background(), e.target.ID); err != nil {
			t.Error(err)
		}
		return inner(ctx, argv)
	}
	s := e.sched(starter, nil)
	e.clk.Advance(time.Second)
	s.Tick(context.Background())
	if log.n() != 1 {
		t.Fatalf("calls %d", log.n())
	}
	if d := nextIn(e); d > 0 {
		t.Fatalf("next run in %v: the failure's backoff overwrote Pull now", d)
	}
}

// A successful drain session prunes old day files under flush.lock.
func TestDrainPrunesOldDayFilesAtTheEnd(t *testing.T) {
	e := newEnv(t)
	e.fill(3)
	old := filepath.Join(e.ob.Dir, "2020-01-01.jsonl")
	if err := os.WriteFile(old, []byte(recLine(100, 1, machineID)+"\n"+recLine(101, 1, machineID)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ep := e.session(faults{}, func(_ *pull.Session, o *client.DrainOptions) { o.KeepDays = 30 })
	if ep.remote.Err != nil || ep.hub.Outcome != pull.OutcomeOK {
		t.Fatalf("%+v", ep)
	}
	if len(e.stored()) != 5 {
		t.Fatalf("stored %d", len(e.stored()))
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old day file not pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.ob.Dir, "2026-10-12.jsonl")); err != nil {
		t.Fatalf("current day file gone: %v", err)
	}
}

// ExecStarter kills ssh when its context ends and Wait returns even if a
// child keeps the pipes open.
func TestExecStarterWaitDelayBoundsAHungChild(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil || testing.Short() {
		t.Skip("needs /bin/sh, and takes the kill grace")
	}
	ctx, cancel := context.WithCancel(context.Background())
	proc, err := pull.ExecStarter("/bin/sh")(ctx, []string{"-c", "sleep 60 & sleep 60"})
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(100*time.Millisecond, cancel)
	done := make(chan struct{})
	go func() { proc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Wait hung on a child that holds the pipes")
	}
}
