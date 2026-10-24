package scan

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

// A session_list describes the sessions as they were when `claude agents` ran, so the
// record is stamped with the start of that run, not with the time collection finished.
func TestAgentsRecordIsStampedWithRunStart(t *testing.T) {
	home, slots := agentsHome(t)
	fc := &fakeClaude{home: home, t: t}
	var mu sync.Mutex
	clock := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	start := clock
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	slow := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		mu.Lock()
		clock = clock.Add(time.Minute) // the run takes a minute
		mu.Unlock()
		return fc.run(ctx, env, name, args...)
	}
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	if _, err := Agents(context.Background(), AgentsOptions{Slots: slots[1:2], Run: slow, Outbox: ob,
		Machine: "m_x", Client: "wy/test", Now: now}); err != nil {
		t.Fatal(err)
	}
	recs := readAll(t, ob)
	if len(recs) != 1 || recs[0].Kind != record.KindSessionList {
		t.Fatalf("%d records", len(recs))
	}
	if want := record.FormatTime(start); recs[0].TS != want {
		t.Fatalf("ts = %s, want the run start %s", recs[0].TS, want)
	}
}
