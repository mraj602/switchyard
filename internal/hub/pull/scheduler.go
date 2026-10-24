package pull

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/drainproto"
	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// Scheduler tuning (machines.md 3.4).
const (
	DefaultInterval = 60 * time.Second
	MaxConcurrent   = 4
	// AuthRetry is how often a target in state `auth` is retried.
	AuthRetry = 15 * time.Minute
	// NewAuthGrace is how long after it was added a never-pulled target's "Permission
	// denied" is not yet `auth` (the key is authorized on the server after registering).
	NewAuthGrace = 2 * time.Minute
	// AliveFile's mtime says a scheduler is running in a hub process (the CLI uses it to
	// decide between asking the hub to run a target and running it in-process).
	AliveFile = "scheduler.alive"
	// AliveWithin is how fresh AliveFile must be.
	AliveWithin = 5 * time.Second
	// SSHConnectAllowance is added to the hello timeout of a real ssh run: the timer starts
	// when ssh is launched, and ssh first needs its connect and key exchange (ConnectTimeout
	// 10 s plus the handshake) before the remote can say hello.
	SSHConnectAllowance = 20 * time.Second
	// killGrace is how long ssh gets to exit after the session ended before it is killed.
	killGrace = 5 * time.Second
)

// Ladder is the backoff after the nth consecutive failure (the last step repeats).
var Ladder = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute}

// Proc is one running ssh.
type Proc interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	// Wait returns the exit status (-1 if not an exit) and the first drainproto.StderrLogBytes
	// of stderr once the process ended.
	Wait() (code int, stderr string, err error)
	Kill()
}

// Starter starts `ssh <argv...>`. Tests supply their own (a fake ssh program, or a pipe to
// client.Drain).
type Starter func(ctx context.Context, argv []string) (Proc, error)

// ExecStarter runs the real program (default "ssh") with os/exec.
func ExecStarter(program string) Starter {
	if program == "" {
		program = "ssh"
	}
	return func(ctx context.Context, argv []string) (Proc, error) {
		// CommandContext kills ssh when ctx ends (Stop, the session limit); WaitDelay makes Wait
		// return even when a child (a ProxyCommand, a ControlMaster) keeps the pipes open.
		cmd := exec.CommandContext(ctx, program, argv...)
		cmd.WaitDelay = killGrace
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		p := &execProc{cmd: cmd, in: in, out: out, errb: &limitBuf{max: drainproto.StderrLogBytes}}
		cmd.Stderr = p.errb
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("could not start %s: %w", program, err)
		}
		return p, nil
	}
}

type limitBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (l *limitBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil // never block the child on a full buffer
}

func (l *limitBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type execProc struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.Reader
	errb *limitBuf
}

func (p *execProc) Stdin() io.WriteCloser { return p.in }
func (p *execProc) Stdout() io.Reader     { return p.out }
func (p *execProc) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}
func (p *execProc) Wait() (int, string, error) {
	err := p.cmd.Wait()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code, err = ee.ExitCode(), nil
		} else {
			code = -1
		}
	}
	return code, p.errb.String(), err
}

// Config configures a Scheduler.
type Config struct {
	Store      *store.Store
	Ingester   *ingest.Ingester
	Dir        string // <state>/pull
	HubVersion string

	SSH     string  // the ssh program, default "ssh"
	Starter Starter // overrides SSH (tests)
	Now     func() time.Time
	Rand    func() float64 // in [0,1), default math/rand
	Logf    func(format string, args ...any)

	Tick        time.Duration // how often due targets are looked for; default 1 s
	Concurrency int           // default MaxConcurrent
	// SessionLimit caps one run (ssh included); default Budget + 2 minutes.
	SessionLimit time.Duration
	// Session fields copied into every session (tests).
	Hook         func(step string) error
	Budget       time.Duration
	MaxBatches   int
	MaxRecords   int
	HelloTimeout time.Duration
	BatchTimeout time.Duration
}

// Scheduler runs due pull targets. It implements api.Puller.
type Scheduler struct {
	cfg Config

	mu       sync.Mutex
	inflight map[string]bool
	busyOnce map[string]bool
	sem      chan struct{}

	kickCh chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var _ api.Puller = (*Scheduler)(nil)

// New returns a stopped scheduler.
func New(cfg Config) *Scheduler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Rand == nil {
		cfg.Rand = defaultRand
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Starter == nil {
		cfg.Starter = ExecStarter(cfg.SSH)
	}
	if cfg.Tick <= 0 {
		cfg.Tick = time.Second
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = MaxConcurrent
	}
	if cfg.SessionLimit <= 0 {
		cfg.SessionLimit = drainproto.Budget + 2*time.Minute
	}
	return &Scheduler{cfg: cfg, inflight: map[string]bool{}, busyOnce: map[string]bool{},
		sem: make(chan struct{}, cfg.Concurrency), kickCh: make(chan struct{}, 1)}
}

// Start runs the scheduler loop in the background until Stop (or ctx ends).
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(s.cfg.Tick)
		defer t.Stop()
		for {
			s.touchAlive()
			s.launch(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-s.kickCh:
			}
		}
	}()
}

// Stop ends the loop and waits for running sessions to finish (their context is cancelled,
// so each ends at its next frame). Call it before closing the store.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	c := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if c != nil {
		c()
	}
	s.wg.Wait()
}

func (s *Scheduler) touchAlive() {
	if s.cfg.Dir == "" {
		return
	}
	if err := os.MkdirAll(s.cfg.Dir, 0o700); err != nil {
		return
	}
	p := filepath.Join(s.cfg.Dir, AliveFile)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.Close()
		}
	}
}

// Alive reports whether a scheduler in a hub process touched dir/scheduler.alive lately.
func Alive(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, AliveFile))
	return err == nil && time.Since(st.ModTime()) < AliveWithin
}

func (s *Scheduler) kick() {
	select {
	case s.kickCh <- struct{}{}:
	default:
	}
}

func (s *Scheduler) frozen(ctx context.Context) bool {
	_, ok, _ := s.cfg.Store.AlertSetting(ctx, SettingFrozen)
	return ok
}

// Tick runs every target that is due now and waits for them. The loop does the same without
// waiting. Nothing runs while the hub is frozen.
func (s *Scheduler) Tick(ctx context.Context) {
	wg := s.launchWG(ctx)
	wg.Wait()
}

func (s *Scheduler) launch(ctx context.Context) { _ = s.launchWG(ctx) }

func (s *Scheduler) launchWG(ctx context.Context) *sync.WaitGroup {
	wg := &sync.WaitGroup{}
	if ctx.Err() != nil || s.frozen(ctx) {
		return wg
	}
	due, err := s.cfg.Store.DuePullTargets(ctx, s.cfg.Now().UnixMilli())
	if err != nil {
		s.cfg.Logf("pull: listing due targets: %v", err)
		return wg
	}
	for _, t := range due {
		select {
		case s.sem <- struct{}{}:
		default:
			return wg // 4 runs at once; the rest wait for the next tick
		}
		if !s.markRunning(t.ID) {
			<-s.sem
			continue
		}
		wg.Add(1)
		s.wg.Add(1)
		go func(t store.PullTarget) {
			defer wg.Done()
			defer s.wg.Done()
			defer func() { <-s.sem; s.unmark(t.ID) }()
			s.run(ctx, t, true)
		}(t)
	}
	return wg
}

func (s *Scheduler) markRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[id] {
		return false
	}
	s.inflight[id] = true
	return true
}

func (s *Scheduler) unmark(id string) {
	s.mu.Lock()
	delete(s.inflight, id)
	s.mu.Unlock()
}

// RunTarget runs one target now and waits, regardless of its next_at and state (but not
// while another run of it is in progress or the hub is frozen). `wy hub pull run` uses it
// when no hub process is running.
func (s *Scheduler) RunTarget(ctx context.Context, t store.PullTarget) error {
	if s.frozen(ctx) {
		return errors.New("the hub is frozen for a move; pulls are paused")
	}
	if !s.markRunning(t.ID) {
		return errors.New("a pull of this target is already running")
	}
	defer s.unmark(t.ID)
	s.run(ctx, t, false)
	return nil
}

// RunNow implements api.Puller: it makes the machine's target due now and wakes the loop.
func (s *Scheduler) RunNow(ctx context.Context, machineID string) (api.PullStatus, error) {
	t, err := s.cfg.Store.PullTargetByMachine(ctx, machineID)
	if errors.Is(err, store.ErrNotFound) {
		return api.PullStatus{}, api.ErrNoPullTarget
	}
	if err != nil {
		return api.PullStatus{}, err
	}
	if t.State == store.PullStateHostKey || t.State == store.PullStateRefused {
		return s.Status(ctx, t), api.ErrPullBlocked
	}
	if t.Enabled {
		if err := s.cfg.Store.SchedulePullNow(ctx, t.ID); err != nil {
			return api.PullStatus{}, err
		}
		s.kick()
		t, _ = s.cfg.Store.PullTargetByID(ctx, t.ID)
	}
	return s.Status(ctx, t), nil
}

// Status is the api.PullStatus of a target (state `frozen` while the hub is frozen).
func (s *Scheduler) Status(ctx context.Context, t store.PullTarget) api.PullStatus {
	return Status(t, s.frozen(ctx))
}

// Status builds api.PullStatus from a row.
func Status(t store.PullTarget, frozen bool) api.PullStatus {
	target := t.User + "@" + t.Host
	if t.Port != 22 {
		target += fmt.Sprintf(":%d", t.Port)
	}
	st := api.PullStatus{Name: t.Name, Target: target, IntervalS: t.IntervalS, State: t.State}
	if frozen && t.Enabled {
		st.State = api.PullFrozen
	}
	ts := func(ms int64) *string {
		if ms == 0 {
			return nil
		}
		v := time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
		return &v
	}
	st.LastTry, st.LastOK, st.NextAt = ts(t.LastTryAt), ts(t.LastOKAt), ts(t.NextAt)
	if t.LastError != "" {
		e := t.LastError
		st.LastError = &e
	}
	return st
}

// run is one session for t, then the row update. scheduled is false for RunTarget (an
// explicit `wy hub pull run`), which always reports the real outcome.
func (s *Scheduler) run(ctx context.Context, t store.PullTarget, scheduled bool) {
	res, stderr, exit, err := s.session(ctx, t)
	s.record(ctx, t, res, stderr, exit, err, scheduled)
}

func (s *Scheduler) session(ctx context.Context, t store.PullTarget) (res Result, stderr string, exit int, err error) {
	exit = -2 // no process
	all, lerr := s.cfg.Store.PullTargets(ctx)
	if lerr != nil {
		return res, "", exit, lerr
	}
	if cerr := WriteConfig(s.cfg.Dir, all); cerr != nil {
		s.cfg.Logf("pull: %v", cerr)
	}
	if verr := ValidateTarget(t); verr != nil {
		return res, "", exit, verr
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.SessionLimit)
	defer cancel()
	helloT := s.cfg.HelloTimeout
	if helloT <= 0 {
		helloT = drainproto.HelloTimeout + SSHConnectAllowance
	}
	proc, serr := s.cfg.Starter(cctx, SSHArgs(s.cfg.Dir, t))
	if serr != nil {
		return res, "", exit, serr
	}
	sess := &Session{Store: s.cfg.Store, Ingester: s.cfg.Ingester, HubVersion: s.cfg.HubVersion, Now: s.cfg.Now,
		Hook: s.cfg.Hook, Budget: s.cfg.Budget, MaxBatches: s.cfg.MaxBatches, MaxRecords: s.cfg.MaxRecords, HelloTimeout: helloT,
		BatchTimeout: s.cfg.BatchTimeout}
	res = sess.Serve(cctx, t, proc.Stdout(), proc.Stdin())
	_ = proc.Stdin().Close()
	type waited struct {
		code   int
		stderr string
		err    error
	}
	wc := make(chan waited, 1)
	go func() {
		c, e, werr := proc.Wait()
		wc <- waited{c, e, werr}
	}()
	select {
	case w := <-wc:
		return res, w.stderr, w.code, w.err
	case <-time.After(killGrace):
		proc.Kill()
		w := <-wc
		return res, w.stderr, w.code, w.err
	}
}

// record turns a session outcome into the target's state and next run (machines.md 3.4).
func (s *Scheduler) record(ctx context.Context, t store.PullTarget, res Result, stderr string, exit int, err error, scheduled bool) {
	now := s.cfg.Now()
	interval := time.Duration(t.IntervalS) * time.Second
	if interval <= 0 {
		interval = DefaultInterval
	}
	fails := t.FailCount + 1
	r := store.PullResult{}
	line := firstLine(stderr)
	reached := res.Machine != "" // a hello arrived: ssh and the forced command worked

	switch {
	case err == nil && res.Err == nil && res.Outcome == OutcomeOK:
		s.setBusy(t.ID, false)
		r = store.PullResult{State: store.PullStateOK, OK: true, NextAt: now.Add(s.jitter(interval)).UnixMilli()}
		if res.More {
			r.NextAt = now.Add(drainproto.MoreDelay).UnixMilli()
		}
	case res.Outcome == OutcomeBusy && res.Err == nil:
		first := !s.setBusy(t.ID, true)
		d := interval
		if first {
			d = drainproto.BusyRetry
		}
		r = store.PullResult{State: store.PullStateOK, OK: true, NextAt: now.Add(d).UnixMilli()}
	case res.Outcome == OutcomeRefused:
		msg := "the remote's machine changed: check it, then wy hub pull remove and add again"
		if res.Code == drainproto.RefuseMachineMismatch && res.Msg != "" {
			msg = "the hub refused the pull: " + res.Msg
		}
		if res.Code == drainproto.RefuseFrozen {
			// A move started between the scheduler's check and the hello: not a failure.
			r = store.PullResult{State: store.PullStateOK, OK: true, NextAt: now.Add(interval).UnixMilli()}
			break
		}
		if res.Code == drainproto.RefuseDisabled {
			r = store.PullResult{State: store.PullStateDisabled, Error: "disabled or revoked", NextAt: now.Add(interval).UnixMilli()}
			break
		}
		if res.Code != drainproto.RefuseMachineMismatch {
			msg = "the hub refused the pull: " + res.Code
		}
		r = store.PullResult{State: store.PullStateRefused, Error: msg, OK: true}
	case strings.Contains(stderr, "Host key verification failed") || strings.Contains(stderr, "HOST IDENTIFICATION HAS CHANGED"):
		r = store.PullResult{State: store.PullStateHostKey, FailCount: fails,
			Error: "host key changed: check the server, then `wy hub pull trust " + t.Name + "`"}
	case scheduled && strings.Contains(stderr, "Permission denied") && t.State == store.PullStateNew && t.LastOKAt == 0 &&
		now.Sub(time.UnixMilli(t.CreatedAt)) < NewAuthGrace:
		// Just registered: `wy machine add` adds the target before it authorizes the key on the
		// server, so the first scheduled try can race it. Stay `new` and retry soon.
		r = store.PullResult{State: store.PullStateNew, FailCount: fails, NextAt: now.Add(Ladder[0]).UnixMilli(),
			Error: sshLine(line) + " (the pull key may not be authorized yet)"}
	case strings.Contains(stderr, "Permission denied"):
		r = store.PullResult{State: store.PullStateAuth, FailCount: fails, NextAt: now.Add(AuthRetry).UnixMilli(),
			Error: sshLine(line)}
	default:
		msg := "pull failed"
		switch {
		case err != nil:
			msg = err.Error()
		case res.Err != nil && reached:
			msg = res.Err.Error()
		case line != "":
			msg = sshLine(line)
		case exit > 0:
			msg = fmt.Sprintf("ssh exited %d", exit)
		case res.Err != nil:
			msg = res.Err.Error()
		}
		d := Ladder[min(fails, len(Ladder))-1]
		r = store.PullResult{State: store.PullStateFailing, FailCount: fails, Error: firstLine(msg), NextAt: now.Add(d).UnixMilli()}
	}
	if res.Outcome == OutcomeNack {
		r.State = store.PullStateFailing
	}
	// A "Pull now" (or enable, trust) that landed while this run was going set next_at: it
	// wins over the next time this run computed.
	r.GuardNextAt, r.WasNextAt = true, t.NextAt
	if err := s.cfg.Store.RecordPullRun(ctx, t.ID, r); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, store.ErrFrozen) {
		s.cfg.Logf("pull %s: recording the run: %v", t.Name, err)
	}
	if r.Error != "" {
		s.cfg.Logf("pull %s: %s (%s)", t.Name, r.Error, r.State)
	}
	if stderr != "" {
		s.cfg.Logf("pull %s: remote stderr: %s", t.Name, truncate(strings.TrimSpace(stderr), drainproto.StderrLogBytes))
	}
}

// setBusy records that the last run was busy and returns the previous value.
func (s *Scheduler) setBusy(id string, v bool) (was bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	was = s.busyOnce[id]
	if v {
		s.busyOnce[id] = true
	} else {
		delete(s.busyOnce, id)
	}
	return was
}

// jitter is d with +-10 %.
func (s *Scheduler) jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.9 + 0.2*s.cfg.Rand()))
}

// sshLine names ssh as the source of an error line, once: ssh's own messages already start
// with "ssh: " ("ssh: Could not resolve hostname …").
func sshLine(line string) string {
	if strings.HasPrefix(line, "ssh: ") {
		return line
	}
	return "ssh: " + line
}
