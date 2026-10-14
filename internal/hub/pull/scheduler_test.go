package pull_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/hub/api"
	"github.com/mraj602/switchyard/internal/hub/pull"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// fakeProc is a scripted ssh process.
type fakeProc struct {
	in     io.WriteCloser
	out    io.Reader
	code   int
	stderr string
	done   chan struct{}
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.in }
func (p *fakeProc) Stdout() io.Reader     { return p.out }
func (p *fakeProc) Kill()                 {}
func (p *fakeProc) Wait() (int, string, error) {
	if p.done != nil {
		<-p.done
	}
	return p.code, p.stderr, nil
}

type nopWC struct{ io.Writer }

func (nopWC) Close() error { return nil }

type starterLog struct {
	mu    sync.Mutex
	calls [][]string
}

func (l *starterLog) add(argv []string) {
	l.mu.Lock()
	l.calls = append(l.calls, append([]string(nil), argv...))
	l.mu.Unlock()
}
func (l *starterLog) n() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.calls) }

// failStarter: ssh exits with code and stderr before any frame.
func failStarter(log *starterLog, code int, stderr string) pull.Starter {
	return func(_ context.Context, argv []string) (pull.Proc, error) {
		log.add(argv)
		return &fakeProc{in: nopWC{io.Discard}, out: strings.NewReader(""), code: code, stderr: stderr}, nil
	}
}

// drainStarter connects the "ssh" to client.Drain on the given outbox, in-process.
func drainStarter(log *starterLog, e *env, mutate func(*client.DrainOptions)) pull.Starter {
	return func(ctx context.Context, argv []string) (pull.Proc, error) {
		log.add(argv)
		r1, w1 := io.Pipe() // remote -> hub
		r2, w2 := io.Pipe() // hub -> remote
		o := client.DrainOptions{Outbox: e.ob, Machine: machineID, Name: "gpu-box", SyVersion: "0.5.0", Now: e.clk.Now}
		if mutate != nil {
			mutate(&o)
		}
		done := make(chan struct{})
		go func() {
			client.Drain(ctx, r2, w1, o)
			w1.Close()
			close(done)
		}()
		return &fakeProc{in: w2, out: r1, done: done}, nil
	}
}

func (e *env) sched(starter pull.Starter, mutate func(*pull.Config)) *pull.Scheduler {
	cfg := pull.Config{Store: e.st, Ingester: e.ing, Dir: e.dir, HubVersion: "0.5.0", Starter: starter, Now: e.clk.Now,
		Rand: func() float64 { return 0.5 }, HelloTimeout: 2 * time.Second, BatchTimeout: 2 * time.Second}
	if mutate != nil {
		mutate(&cfg)
	}
	return pull.New(cfg)
}

func (e *env) row() store.PullTarget {
	e.t.Helper()
	tg, err := e.st.PullTargetByID(context.Background(), e.target.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return tg
}

func nextIn(e *env) time.Duration {
	return time.UnixMilli(e.row().NextAt).Sub(e.clk.Now())
}

func TestSchedulerRunsDueTargetsOnInterval(t *testing.T) {
	e := newEnv(t)
	ids := e.fill(30)
	var log starterLog
	s := e.sched(drainStarter(&log, e, nil), nil)
	ctx := context.Background()

	s.Tick(ctx)
	if log.n() != 1 {
		t.Fatalf("starter calls %d", log.n())
	}
	argv := log.calls[0]
	want := []string{"-F", filepath.Join(e.dir, "ssh_config"), "-T", "sy-" + e.target.ID, ".local/bin/sy", "outbox", "drain", "--stdio"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv %q, want %q", argv, want)
	}
	e.assertOnceInOrder(ids)
	row := e.row()
	if row.State != store.PullStateOK || row.FailCount != 0 || row.LastOKAt == 0 || row.LastError != "" {
		t.Fatalf("%+v", row)
	}
	if d := nextIn(e); d != 60*time.Second { // Rand 0.5 = no jitter
		t.Fatalf("next run in %v, want the 60 s interval", d)
	}
	s.Tick(ctx) // not due yet
	if log.n() != 1 {
		t.Fatal("ran before the interval")
	}
	e.clk.Advance(59 * time.Second)
	s.Tick(ctx)
	if log.n() != 1 {
		t.Fatal("ran 1 s early")
	}
	e.clk.Advance(time.Second)
	s.Tick(ctx)
	if log.n() != 2 {
		t.Fatalf("calls %d, want a run at the interval", log.n())
	}
	// ssh_config and known_hosts were written before the run.
	if st, err := os.Stat(filepath.Join(e.dir, "ssh_config")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("ssh_config: %v %v", st, err)
	}
}

func TestSchedulerJitterStaysWithinTenPercent(t *testing.T) {
	for _, r := range []float64{0, 0.999999} {
		e := newEnv(t)
		e.fill(1)
		var log starterLog
		s := e.sched(drainStarter(&log, e, nil), func(c *pull.Config) { c.Rand = func() float64 { return r } })
		s.Tick(context.Background())
		d := nextIn(e)
		if d < 54*time.Second || d > 66*time.Second {
			t.Fatalf("rand %v: next in %v", r, d)
		}
	}
}

func TestSchedulerBackoffLadder(t *testing.T) {
	e := newEnv(t)
	var log starterLog
	s := e.sched(failStarter(&log, 255, "ssh: connect to host gpu-box port 22: Connection refused"), nil)
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		s.Tick(context.Background())
		row := e.row()
		if row.State != store.PullStateFailing || row.FailCount != i+1 {
			t.Fatalf("step %d: %+v", i, row)
		}
		if !strings.Contains(row.LastError, "Connection refused") {
			t.Fatalf("last_error %q", row.LastError)
		}
		if d := nextIn(e); d != w {
			t.Fatalf("step %d: next in %v, want %v", i, d, w)
		}
		e.clk.Advance(w - time.Second)
		s.Tick(context.Background()) // not yet
		if log.n() != i+1 {
			t.Fatalf("step %d: ran early", i)
		}
		e.clk.Advance(time.Second)
	}
	// Success resets the ladder.
	e.fill(2)
	s2 := e.sched(drainStarter(&log, e, nil), nil)
	s2.Tick(context.Background())
	if row := e.row(); row.State != store.PullStateOK || row.FailCount != 0 || row.LastError != "" {
		t.Fatalf("%+v", row)
	}
}

func TestSchedulerHostKeyStopsRetrying(t *testing.T) {
	e := newEnv(t)
	var log starterLog
	s := e.sched(failStarter(&log, 255, "Host key verification failed."), nil)
	s.Tick(context.Background())
	row := e.row()
	if row.State != store.PullStateHostKey || !strings.Contains(row.LastError, "sy hub pull trust gpu-box") {
		t.Fatalf("%+v", row)
	}
	for i := 0; i < 5; i++ {
		e.clk.Advance(time.Hour)
		s.Tick(context.Background())
	}
	if log.n() != 1 {
		t.Fatalf("retried a host_key failure %d times", log.n())
	}
	// The API refuses to run it; the owner re-pins, and it runs again.
	if _, err := s.RunNow(context.Background(), machineID); !errors.Is(err, api.ErrPullBlocked) {
		t.Fatalf("RunNow: %v", err)
	}
	if err := e.st.TrustPullKeys(context.Background(), e.target.ID, []string{hostKeyB}); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if log.n() != 2 {
		t.Fatalf("calls %d after trust", log.n())
	}
	// The changed-identification warning is the same case.
	e2 := newEnv(t)
	var log2 starterLog
	s2 := e2.sched(failStarter(&log2, 255, "@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@"), nil)
	s2.Tick(context.Background())
	if e2.row().State != store.PullStateHostKey {
		t.Fatalf("%+v", e2.row())
	}
}

func TestSchedulerAuthRetriesEveryFifteenMinutes(t *testing.T) {
	e := newEnv(t)
	var log starterLog
	s := e.sched(failStarter(&log, 255, "dev@gpu-box: Permission denied (publickey)."), nil)
	// Just added, the key may not be authorized yet: stays new, retried after 30 s.
	s.Tick(context.Background())
	if row := e.row(); row.State != store.PullStateNew || nextIn(e) != pull.Ladder[0] || !strings.Contains(row.LastError, "not be authorized yet") {
		t.Fatalf("within the grace: %+v next %v", row, nextIn(e))
	}
	e.clk.Advance(pull.NewAuthGrace)
	s.Tick(context.Background())
	if row := e.row(); row.State != store.PullStateAuth || nextIn(e) != 15*time.Minute {
		t.Fatalf("%+v next %v", row, nextIn(e))
	}
	e.clk.Advance(15 * time.Minute)
	s.Tick(context.Background())
	if log.n() != 3 || e.row().State != store.PullStateAuth {
		t.Fatalf("calls %d %+v", log.n(), e.row())
	}
}

func TestSchedulerMoreRunsAgainAfterOneSecond(t *testing.T) {
	e := newEnv(t)
	ids := e.fill(120)
	var log starterLog
	s := e.sched(drainStarter(&log, e, nil), func(c *pull.Config) { c.MaxBatches = 1; c.MaxRecords = 60 })
	s.Tick(context.Background())
	if d := nextIn(e); d != time.Second {
		t.Fatalf("next in %v, want 1 s after a budget stop", d)
	}
	e.clk.Advance(time.Second)
	s.Tick(context.Background())
	e.assertOnceInOrder(ids)
	if d := nextIn(e); d != 60*time.Second {
		t.Fatalf("after draining, next in %v", d)
	}
}

func TestSchedulerBusyRetriesOnceAfterFiveSeconds(t *testing.T) {
	e := newEnv(t)
	e.fill(3)
	lock, ok, err := e.ob.TryFlushLock()
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	var log starterLog
	s := e.sched(drainStarter(&log, e, nil), nil)
	s.Tick(context.Background())
	if d := nextIn(e); d != 5*time.Second {
		t.Fatalf("first busy: next in %v", d)
	}
	e.clk.Advance(5 * time.Second)
	s.Tick(context.Background())
	if d := nextIn(e); d != 60*time.Second {
		t.Fatalf("second busy: next in %v, want the interval", d)
	}
	lock.Release()
	e.clk.Advance(time.Minute)
	s.Tick(context.Background())
	if len(e.stored()) != 3 || e.row().State != store.PullStateOK {
		t.Fatalf("stored %d %+v", len(e.stored()), e.row())
	}
}

func TestSchedulerPausesWhileFrozen(t *testing.T) {
	e := newEnv(t)
	e.fill(3)
	var log starterLog
	s := e.sched(drainStarter(&log, e, nil), nil)
	ctx := context.Background()
	if err := e.st.Write(ctx, func(tx *store.Tx) error {
		return tx.PutAlertSetting(pull.SettingFrozen, []byte(`{"since":1,"moved_to":null}`), 1)
	}); err != nil {
		t.Fatal(err)
	}
	s.Tick(ctx)
	if log.n() != 0 {
		t.Fatal("pulled while frozen")
	}
	if err := s.RunTarget(ctx, e.target); err == nil {
		t.Fatal("RunTarget while frozen")
	}
	if st := s.Status(ctx, e.row()); st.State != api.PullFrozen {
		t.Fatalf("status %+v", st)
	}
	// Unfrozen: the target is still due and runs.
	if err := e.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM setting WHERE key = ?`, pull.SettingFrozen)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s.Tick(ctx)
	if log.n() != 1 || len(e.stored()) != 3 {
		t.Fatalf("calls %d stored %d", log.n(), len(e.stored()))
	}
}

func TestSchedulerRunNow(t *testing.T) {
	e := newEnv(t)
	var log starterLog
	s := e.sched(failStarter(&log, 255, "connection refused"), nil)
	ctx := context.Background()
	if _, err := s.RunNow(ctx, "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4Z"); !errors.Is(err, api.ErrNoPullTarget) {
		t.Fatalf("unknown machine: %v", err)
	}
	s.Tick(ctx) // fails, next in 30 s
	if nextIn(e) <= 0 {
		t.Fatal("setup")
	}
	st, err := s.RunNow(ctx, machineID)
	if err != nil || st.Name != "gpu-box" || st.NextAt == nil {
		t.Fatalf("%+v %v", st, err)
	}
	if nextIn(e) != 0 {
		t.Fatalf("next_at not now: %v", nextIn(e))
	}
	s.Tick(ctx)
	if log.n() != 2 {
		t.Fatalf("calls %d: RunNow must make the target due immediately", log.n())
	}
}

func TestSchedulerStartStopRunsInBackground(t *testing.T) {
	e := newEnv(t)
	e.fill(3)
	var log starterLog
	s := e.sched(drainStarter(&log, e, nil), func(c *pull.Config) { c.Tick = 10 * time.Millisecond })
	s.Start(context.Background())
	deadline := time.Now().Add(10 * time.Second)
	for len(e.stored()) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	s.Stop()
	if len(e.stored()) != 3 {
		t.Fatal("background scheduler did not pull")
	}
	if !pull.Alive(e.dir) {
		t.Fatal("scheduler.alive not touched")
	}
	s.Stop() // idempotent
}

func TestSchedulerAtMostFourRunsAtOnceAndOnePerTarget(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		e.addTarget(n, "")
	}
	var cur, peak atomic.Int32
	release := make(chan struct{})
	var log starterLog
	starter := func(_ context.Context, argv []string) (pull.Proc, error) {
		log.add(argv)
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		cur.Add(-1)
		return &fakeProc{in: nopWC{io.Discard}, out: strings.NewReader(""), code: 255, stderr: "refused"}, nil
	}
	s := e.sched(starter, nil)
	done := make(chan struct{})
	go func() { s.Tick(context.Background()); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for cur.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // a fifth would have started by now
	if cur.Load() != 4 {
		t.Fatalf("%d runs at once, want exactly 4", cur.Load())
	}
	// A second tick while they run must not start a second run of any target.
	go s.Tick(context.Background())
	time.Sleep(100 * time.Millisecond)
	close(release)
	<-done
	if peak.Load() > 4 {
		t.Fatalf("peak %d", peak.Load())
	}
	seen := map[string]int{}
	for _, c := range log.calls {
		seen[c[3]]++
	}
	for a, n := range seen {
		if n > 1 {
			t.Fatalf("%s ran %d times", a, n)
		}
	}
}

func TestSchedulerMismatchedMachineGoesToRefused(t *testing.T) {
	e := newEnv(t)
	var log starterLog
	s := e.sched(drainStarter(&log, e, func(o *client.DrainOptions) { o.Machine = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4Z" }), nil)
	s.Tick(context.Background())
	row := e.row()
	if row.State != store.PullStateRefused {
		t.Fatalf("%+v", row)
	}
	e.clk.Advance(time.Hour)
	s.Tick(context.Background())
	if log.n() != 1 {
		t.Fatal("a refused target must not retry until the owner acts")
	}
}

func TestSchedulerDisabledTargetIsNotRun(t *testing.T) {
	e := newEnv(t)
	var log starterLog
	s := e.sched(failStarter(&log, 255, "x"), nil)
	if err := e.st.SetPullEnabled(context.Background(), e.target.ID, false); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if log.n() != 0 || e.row().State != store.PullStateDisabled {
		t.Fatalf("calls %d %+v", log.n(), e.row())
	}
	if err := e.st.SetPullEnabled(context.Background(), e.target.ID, true); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if log.n() != 1 {
		t.Fatal("enabled target did not run")
	}
}

// A fake `ssh` program records its argv and stdin; the scheduler runs it with os/exec.
func TestSchedulerWithFakeSSHProgram(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	e := newEnv(t)
	bin := t.TempDir()
	rec := filepath.Join(bin, "rec")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + rec + ".argv; done\n" +
		"cat > " + rec + ".stdin &\n" +
		"echo 'dev@gpu-box: Permission denied (publickey).' >&2\n" +
		"exit 255\n"
	ssh := filepath.Join(bin, "ssh")
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s := e.sched(nil, func(c *pull.Config) { c.SSH = ssh })
	e.clk.Advance(pull.NewAuthGrace) // past the grace for a just-added target
	s.Tick(context.Background())
	argv, _ := os.ReadFile(rec + ".argv")
	got := strings.Split(strings.TrimSpace(string(argv)), "\n")
	want := []string{"-F", filepath.Join(e.dir, "ssh_config"), "-T", "sy-" + e.target.ID, ".local/bin/sy", "outbox", "drain", "--stdio"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv %q", got)
	}
	row := e.row()
	if row.State != store.PullStateAuth || !strings.Contains(row.LastError, "Permission denied") {
		t.Fatalf("%+v", row)
	}
	// Nothing in argv looks like a secret: only fixed words, the config path and the alias.
	for _, a := range got {
		if strings.Contains(a, "sy_m_") || strings.Contains(a, "token") || strings.Contains(a, "BEGIN") {
			t.Fatalf("suspicious argv word %q", a)
		}
	}
}

func TestSchedulerSSHMissingIsAFailureNotAPanic(t *testing.T) {
	e := newEnv(t)
	s := e.sched(nil, func(c *pull.Config) { c.SSH = filepath.Join(t.TempDir(), "no-such-ssh") })
	s.Tick(context.Background())
	row := e.row()
	if row.State != store.PullStateFailing || !strings.Contains(row.LastError, "could not start") {
		t.Fatalf("%+v", row)
	}
}
