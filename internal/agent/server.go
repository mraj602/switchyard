package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/peers"
)

// Config is what Run needs. Everything environmental is injectable.
type Config struct {
	Socket string
	// Load builds the peers environment for one request (so config changes apply without a
	// restart). Its StartPID is replaced by the connecting process's pid.
	Load func() (env peers.Env, enabled bool, err error)
	// Link is the link client (nil: same-machine only).
	Link LinkClient
	// LogPath is <state>/logs/agent.log; empty logs nowhere.
	LogPath string
	Version string
	// UID is the only uid served (default: the process's own).
	UID int
	// PeerCred reads the connecting process's pid and uid (default peers.PeerPID).
	PeerCred func(*net.UnixConn) (pid, uid int, err error)
	// Tick is the housekeeping period (default 1 minute).
	Tick time.Duration
	Now  func() time.Time
}

type server struct {
	cfg     Config
	log     io.Writer
	started time.Time
	sendMu  sync.Mutex // threads.json and limits.json are read, changed and written per send
}

// Run serves the control socket until ctx is done, then removes the socket. It refuses to
// start when another agent answers on the socket.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Load == nil || cfg.Socket == "" {
		return errors.New("agent: Config needs Socket and Load")
	}
	if cfg.PeerCred == nil {
		cfg.PeerCred = peers.PeerPID
	}
	if cfg.UID == 0 {
		cfg.UID = os.Getuid()
	}
	if cfg.Tick <= 0 {
		cfg.Tick = time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &server{cfg: cfg, log: io.Discard, started: cfg.Now()}
	if cfg.LogPath != "" {
		l, err := newRotatingLog(cfg.LogPath, 5<<20, 3)
		if err != nil {
			return err
		}
		defer l.Close()
		s.log = l
	}
	ln, err := listen(cfg.Socket)
	if err != nil {
		return err
	}
	defer os.Remove(cfg.Socket)
	s.logf("start pid=%d socket=%s", os.Getpid(), cfg.Socket)

	var wg sync.WaitGroup
	if cfg.Link != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := cfg.Link.Run(ctx); err != nil && ctx.Err() == nil {
				s.logf("link stopped: %v", err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(cfg.Tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.tick()
			}
		}
	}()
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			ln.Close()
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func() { defer wg.Done(); s.handle(c.(*net.UnixConn)) }()
	}
	wg.Wait()
	s.logf("stop")
	return nil
}

// listen makes the socket folder (0700) and the socket (0600), replacing a stale socket file
// but never one something answers on.
func listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("another sy agent is already listening on %s", path)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (s *server) logf(format string, a ...any) {
	fmt.Fprintf(s.log, "%s %s\n", s.cfg.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

// tick is the 1-minute housekeeping (peers.md 2.2): session files older than 7 days go.
// Thread expiry happens on every send; the link client adds its own retries later.
func (s *server) tick() {
	if env, _, err := s.cfg.Load(); err == nil {
		peers.PruneSessionFiles(env.State, s.cfg.Now())
	}
}

func fail(code, format string, a ...any) Reply {
	return Reply{Error: &Error{Code: code, Detail: fmt.Sprintf(format, a...)}}
}

func (s *server) handle(c *net.UnixConn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(90 * time.Second))
	enc := func(r Reply) {
		b, _ := json.Marshal(r)
		_, _ = c.Write(append(b, '\n'))
	}
	pid, uid, err := s.cfg.PeerCred(c)
	if err != nil {
		enc(fail(peers.CodeFailed, "cannot read the caller's credentials: %v", err))
		return
	}
	if uid != s.cfg.UID {
		s.logf("refused uid=%d", uid)
		enc(fail(peers.CodeFailed, "another user"))
		return
	}
	line, err := bufio.NewReaderSize(io.LimitReader(c, MaxLine), 64<<10).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		enc(fail(peers.CodeFailed, "bad request"))
		return
	}
	enc(s.dispatch(req, pid))
}

func (s *server) dispatch(req Request, pid int) Reply {
	env, enabled, err := s.cfg.Load()
	if err != nil {
		return fail(peers.CodeFailed, "%v", err)
	}
	if !enabled {
		return fail(peers.CodeOff, "peers are not enabled on this machine")
	}
	env.StartPID = pid
	switch req.Op {
	case OpState:
		st := LinkStatus{State: "none"}
		if s.cfg.Link != nil {
			st = s.cfg.Link.Status()
		}
		return Reply{State: &State{PID: os.Getpid(), Started: s.started.UTC().Format(time.RFC3339), Version: s.cfg.Version,
			Socket: s.cfg.Socket, Link: st}}
	case OpOff, OpOn:
		// Terminal only: a session may not switch the machine's messaging on or off.
		if _, rf := env.Whoami(); rf == nil || rf.Code != peers.CodeNotASession {
			return fail(peers.CodeNotASession, "sy peers %s runs at a terminal, not inside a session", req.Op)
		}
		if err := env.SetOff(req.Op == OpOff); err != nil {
			return fail(peers.CodeFailed, "%v", err)
		}
		return Reply{OK: true}
	case OpWhoami, OpList, OpSend:
	default:
		return fail(peers.CodeUnsupported, "op %q is not supported by this agent", req.Op)
	}

	if req.Op == OpList {
		// A list from a terminal (--all) has no session to check.
		if req.ClaudePID != 0 {
			if _, rf := s.caller(env, req); rf != nil {
				return Reply{Error: &Error{Code: rf.Code, Detail: rf.Detail}}
			}
		}
		entries, asked, rf := env.List(req.All)
		if rf != nil {
			return Reply{Error: &Error{Code: rf.Code, Detail: rf.Detail}}
		}
		return Reply{Entries: entries, Asked: asked}
	}
	caller, rf := s.caller(env, req)
	if rf != nil {
		return Reply{Error: &Error{Code: rf.Code, Detail: rf.Detail}}
	}
	if req.Op == OpWhoami {
		return Reply{Session: &SessionInfo{Session: caller.Entry.SessionID, PID: caller.ClaudePID, ConfigDir: caller.Slot.Name}}
	}
	s.sendMu.Lock()
	res, rf := env.Send(caller, req.To, req.Text)
	s.sendMu.Unlock()
	if rf != nil {
		s.logf("send from=%s status=%s size=%d", caller.Entry.SessionID, rf.Code, len(req.Text))
		return Reply{Error: &Error{Code: rf.Code, Detail: rf.Detail}}
	}
	s.logf("send msg=%s thread=%s from=%s to=%s status=delivered size=%d", res.Msg, res.Thread, res.Sender.Entry.SessionID, res.Target.Entry.SessionID, len(req.Text))
	return Reply{Msg: res.Msg, Thread: res.Thread, Status: "delivered", To: res.Target.Handle, HeldLikely: res.HeldLikely}
}

// caller identifies the session the connecting process is inside, and refuses a request that
// names a different one (peers.md 2.3).
func (s *server) caller(env peers.Env, req Request) (peers.Caller, *peers.Refusal) {
	c, rf := env.Whoami()
	if rf != nil {
		return peers.Caller{}, rf
	}
	if req.ClaudePID != 0 && req.ClaudePID != c.ClaudePID {
		return peers.Caller{}, peers.Refuse(peers.CodeNotASession, "the caller is not inside the session of pid %d", req.ClaudePID)
	}
	return c, nil
}
