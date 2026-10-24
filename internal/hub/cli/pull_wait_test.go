package cli

import (
	"context"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
)

// A freshly registered target is due at once, so the hub's scheduler starts a pull before
// `wy machine add` has authorized the key. `pull run --wait` called meanwhile must not take
// that stale run's outcome for its own: it waits for a run that started after the call.
func TestWaitForRunIgnoresRunInFlightBeforeTheCall(t *testing.T) {
	ctx := context.Background()
	f := newPullFixture(t)
	st, err := store.Open(ctx, f.db, store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tg, err := st.AddPullTarget(ctx, store.PullTarget{Name: "srv1", Host: "h", User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	// The scheduler's run: it read the row (next_at = registration time) and is in ssh.
	staleNext := tg.NextAt
	time.Sleep(3 * time.Millisecond)

	// `pull run --wait` schedules a run now.
	at, err := st.SchedulePullAt(ctx, tg.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- waitForRun(ctx, st, tg.ID, at, time.Now().Add(10*time.Second), 5*time.Millisecond)
	}()

	// The in-flight run fails with Permission denied and records itself (the "new" grace).
	time.Sleep(5 * time.Millisecond)
	far := time.Now().Add(30 * time.Second).UnixMilli()
	if err := st.RecordPullRun(ctx, tg.ID, store.PullResult{State: store.PullStateNew, FailCount: 1, NextAt: far,
		Error: "ssh: Permission denied", GuardNextAt: true, WasNextAt: staleNext}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("waitForRun returned on a run that started before the call")
	case <-time.After(150 * time.Millisecond):
	}

	// The fresh run (started after the authorize) records with the scheduled next_at.
	if err := st.RecordPullRun(ctx, tg.ID, store.PullResult{State: store.PullStateOK, OK: true, NextAt: far,
		GuardNextAt: true, WasNextAt: at}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForRun did not return after the fresh run")
	}
}
