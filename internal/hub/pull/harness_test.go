package pull_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	"github.com/mraj602/watchyard/internal/hub/pull"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/outbox"
)

const machineID = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"

// fakeKey builds a syntactically valid ed25519 public host key (never used to connect).
func fakeKey(seed byte) string {
	blob := []byte{0, 0, 0, 11}
	blob = append(blob, "ssh-ed25519"...)
	blob = append(blob, 0, 0, 0, 32)
	for i := 0; i < 32; i++ {
		blob = append(blob, seed+byte(i))
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
}

var hostKeyA, hostKeyB = fakeKey(1), fakeKey(100)

// fakeClock is shared by the store, the scheduler and sessions.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type env struct {
	t      *testing.T
	clk    *fakeClock
	st     *store.Store
	ing    *ingest.Ingester
	dir    string // pull folder
	target store.PullTarget
	ob     *outbox.Outbox
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := newClock()
	root := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(root, "hub.db"), store.Options{Now: clk.Now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, clk: clk, st: st, ing: &ingest.Ingester{Store: st, Now: clk.Now}, dir: filepath.Join(root, "pull"),
		ob: outbox.New(filepath.Join(root, "remote", "outbox"))}
	e.target = e.addTarget("gpu-box", machineID)
	return e
}

func (e *env) addTarget(name, machine string) store.PullTarget {
	e.t.Helper()
	tg, err := e.st.AddPullTarget(context.Background(), store.PullTarget{Name: name, MachineID: machine, Host: name + ".example", User: "dev",
		HostKeys: []string{hostKeyA}})
	if err != nil {
		e.t.Fatal(err)
	}
	return tg
}

func rid(i int) string { return fmt.Sprintf("01JA2XQ8R7Y5V0Z6M3K1T%05d", i) }

func recLine(i, v int, machine string) string {
	return fmt.Sprintf(`{"v":%d,"id":%q,"kind":"hook_event","ts":"2026-10-12T10:%02d:%02d.000Z","machine":%q,"payload":{"hook_event_name":"Stop","session_id":"s1"}}`,
		v, rid(i), (i/60)%60, i%60, machine)
}

// fill writes n records (ids rid(0..n-1)) in one day file of the remote outbox.
func (e *env) fill(n int) []string { return e.fillV(n, nil) }

// fillV is fill with per-index record v (default 1).
func (e *env) fillV(n int, vs map[int]int) []string {
	e.t.Helper()
	if err := os.MkdirAll(e.ob.Dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	var sb strings.Builder
	var ids []string
	for i := 0; i < n; i++ {
		v := 1
		if x, ok := vs[i]; ok {
			v = x
		}
		sb.WriteString(recLine(i, v, machineID) + "\n")
		ids = append(ids, rid(i))
	}
	if err := os.WriteFile(filepath.Join(e.ob.Dir, "2026-10-12.jsonl"), []byte(sb.String()), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return ids
}

// stored returns the record ids in the hub in rowid (arrival) order.
func (e *env) stored() []string {
	e.t.Helper()
	rows, err := e.st.R().QueryContext(context.Background(), `SELECT id FROM record ORDER BY rowid`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// endpoint is how a run ended on each side.
type endpoint struct {
	remote client.DrainResult
	hub    pull.Result
}

type faults struct {
	remote func(step string) error
	hub    func(step string) error
}

// crashAt returns a hook that fails with err at the nth call of step (1-based).
func crashAt(step string, nth int, err error) func(string) error {
	n := 0
	return func(s string) error {
		if s != step {
			return nil
		}
		n++
		if n == nth {
			return err
		}
		return nil
	}
}

var errBoom = errors.New("boom")

// The budgets are generous on purpose: the store is modernc SQLite, which under -race costs ~30 ms
// per record (a 250-record fill takes ~7 s idle, several times that on a loaded CI runner). A
// 20 s budget flaked there (TestSessionFreezeMidSessionRefusesFrozen, "context deadline exceeded").
// They only bound a real hang; nothing here waits on them.
//
// session runs one drain session between client.Drain and pull.Session over pipes. A side
// that "crashes" (its hook errors) has its pipe ends closed, as a dying process would.
func (e *env) session(f faults, mutate func(*pull.Session, *client.DrainOptions)) endpoint {
	e.t.Helper()
	r1, w1 := io.Pipe() // remote -> hub
	r2, w2 := io.Pipe() // hub -> remote
	sess := &pull.Session{Store: e.st, Ingester: e.ing, HubVersion: "0.5.0", Now: e.clk.Now, MaxRecords: 100,
		HelloTimeout: 60 * time.Second, BatchTimeout: 60 * time.Second, Hook: f.hub}
	dopts := client.DrainOptions{Outbox: e.ob, Machine: machineID, Name: "gpu-box", WyVersion: "0.5.0", Now: e.clk.Now,
		StartTimeout: 60 * time.Second, AckTimeout: 60 * time.Second, Hook: f.remote}
	if mutate != nil {
		mutate(sess, &dopts)
	}
	var ep endpoint
	var wg sync.WaitGroup
	wg.Add(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	go func() {
		defer wg.Done()
		ep.remote = client.Drain(ctx, r2, w1, dopts)
		w1.Close() // the remote process exits
		r2.Close()
	}()
	go func() {
		defer wg.Done()
		ep.hub = sess.Serve(ctx, e.target, r1, w2)
		w2.Close() // ssh ends, remote's stdin closes
		r1.Close()
	}()
	wg.Wait()
	return ep
}

// drainAll runs clean sessions until the outbox is drained (at most 50).
func (e *env) drainAll() {
	e.t.Helper()
	for i := 0; i < 50; i++ {
		ep := e.session(faults{}, nil)
		if ep.remote.Err != nil || ep.hub.Err != nil {
			e.t.Fatalf("clean session %d failed: remote %v hub %v", i, ep.remote.Err, ep.hub.Err)
		}
		if ep.remote.Reason == "drained" {
			return
		}
	}
	e.t.Fatal("outbox never drained")
}

// assertOnceInOrder checks the end state: every id stored exactly once in outbox order and
// the cursor at the end of the day file.
func (e *env) assertOnceInOrder(want []string) {
	e.t.Helper()
	got := e.stored()
	if len(got) != len(want) {
		e.t.Fatalf("hub stored %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			e.t.Fatalf("record %d is %s, want %s (order or duplicate)", i, got[i], want[i])
		}
	}
	st, err := e.ob.Stat()
	if err != nil {
		e.t.Fatal(err)
	}
	if st.UnflushedBytes != 0 {
		e.t.Fatalf("cursor not at the end: %d bytes unflushed", st.UnflushedBytes)
	}
}
