package agent

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/peers"
)

// noProc is a process tree with nobody in it: every caller is "not a session".
type noProc struct{}

func (noProc) Info(int) (peers.ProcInfo, error) {
	return peers.ProcInfo{}, errors.New("no such process")
}
func (noProc) ConfigDir(int) (string, bool, error) { return "", false, errors.New("no") }
func (noProc) StartTime(int) (string, error)       { return "", errors.New("no") }

type fakeLink struct {
	started chan struct{}
}

func (l *fakeLink) Run(ctx context.Context) error {
	close(l.started)
	<-ctx.Done()
	return nil
}
func (l *fakeLink) Status() LinkStatus { return LinkStatus{State: "fake", Detail: "d"} }

type running struct {
	sock   string
	state  string
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, mod func(*Config)) *running {
	t.Helper()
	base, err := os.MkdirTemp("", "sya")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	r := &running{sock: filepath.Join(base, "run", "sy", "agent.sock"), state: filepath.Join(base, "state"), done: make(chan error, 1)}
	cfg := Config{
		Socket: r.sock,
		Load: func() (peers.Env, bool, error) {
			return peers.Env{State: r.state, Proc: noProc{}, StartPID: 1}, true, nil
		},
		LogPath: filepath.Join(r.state, "logs", "agent.log"),
		Tick:    20 * time.Millisecond,
	}
	if mod != nil {
		mod(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { r.done <- Run(ctx, cfg) }()
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("unix", r.sock); err == nil {
			c.Close()
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent did not start")
	return nil
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not stop")
	}
}

func TestSocketModesAndState(t *testing.T) {
	l := &fakeLink{started: make(chan struct{})}
	r := start(t, func(c *Config) { c.Link = l; c.Version = "9.9" })
	fi, err := os.Stat(r.sock)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode %v %v", fi, err)
	}
	if di, _ := os.Stat(filepath.Dir(r.sock)); di.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", di.Mode())
	}
	rep, err := Call(r.sock, Request{Op: OpState}, time.Second)
	if err != nil || rep.State == nil || rep.State.PID != os.Getpid() || rep.State.Version != "9.9" || rep.State.Link.State != "fake" {
		t.Fatalf("state %+v %v", rep, err)
	}
	select {
	case <-l.started:
	case <-time.After(time.Second):
		t.Error("the link client was not started")
	}
	r.stop(t)
	if _, err := os.Stat(r.sock); !os.IsNotExist(err) {
		t.Errorf("socket left behind: %v", err)
	}
	if _, err := Call(r.sock, Request{Op: OpState}, time.Second); !errors.Is(err, ErrNoAgent) {
		t.Errorf("after stop: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(r.state, "logs", "agent.log"))
	if !strings.Contains(string(b), " start pid=") || !strings.Contains(string(b), " stop") {
		t.Errorf("log: %q", b)
	}
}

func TestNoLinkState(t *testing.T) {
	r := start(t, nil)
	defer r.stop(t)
	rep, _ := Call(r.sock, Request{Op: OpState}, time.Second)
	if rep.State == nil || rep.State.Link.State != "none" {
		t.Errorf("%+v", rep)
	}
}

func TestRefusals(t *testing.T) {
	r := start(t, nil)
	defer r.stop(t)
	// Outside any session: whoami, send and a session-scoped list are refused; off/on need a
	// terminal and get through.
	for _, req := range []Request{{Op: OpWhoami}, {Op: OpSend, To: "x", Text: "y"}, {Op: OpList}} {
		rep, err := Call(r.sock, req, time.Second)
		if err != nil || rep.Error == nil || rep.Error.Code != peers.CodeNotASession {
			t.Errorf("%s: %+v %v", req.Op, rep, err)
		}
	}
	if rep, _ := Call(r.sock, Request{Op: "pause"}, time.Second); rep.Error == nil || rep.Error.Code != peers.CodeUnsupported {
		t.Errorf("pause: %+v", rep)
	}
	if rep, _ := Call(r.sock, Request{Op: OpOff}, time.Second); !rep.OK {
		t.Errorf("off: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(r.state, "peers", "off")); err != nil {
		t.Errorf("off file: %v", err)
	}
	if rep, _ := Call(r.sock, Request{Op: OpOn}, time.Second); !rep.OK {
		t.Errorf("on: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(r.state, "peers", "off")); !os.IsNotExist(err) {
		t.Errorf("off file kept: %v", err)
	}
	// A named session the caller is not inside.
	if rep, _ := Call(r.sock, Request{Op: OpList, ClaudePID: 4242}, time.Second); rep.Error == nil {
		t.Errorf("list with a foreign claude_pid: %+v", rep)
	}
}

func TestOtherUserRefused(t *testing.T) {
	r := start(t, func(c *Config) {
		c.PeerCred = func(*net.UnixConn) (int, int, error) { return 1, os.Getuid() + 1, nil }
	})
	defer r.stop(t)
	rep, err := Call(r.sock, Request{Op: OpState}, time.Second)
	// The agent refuses and closes at once. If it closes before the client checks who serves
	// the socket, macOS cannot report a closed peer's uid and the client refuses first (also
	// ErrNoAgent, nothing sent). Either way no state comes back.
	if errors.Is(err, ErrNoAgent) {
		return
	}
	if err != nil || rep.Error == nil || rep.State != nil {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestNotEnabled(t *testing.T) {
	r := start(t, func(c *Config) {
		c.Load = func() (peers.Env, bool, error) { return peers.Env{}, false, nil }
	})
	defer r.stop(t)
	if rep, _ := Call(r.sock, Request{Op: OpState}, time.Second); rep.Error == nil || rep.Error.Code != peers.CodeOff {
		t.Errorf("%+v", rep)
	}
}

func TestBadRequest(t *testing.T) {
	r := start(t, nil)
	defer r.stop(t)
	c, err := net.Dial("unix", r.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("not json\n"))
	buf := make([]byte, 200)
	n, _ := c.Read(buf)
	if !strings.Contains(string(buf[:n]), `"error"`) {
		t.Errorf("reply %q", buf[:n])
	}
}

func TestStaleSocketReplacedLiveOneRefused(t *testing.T) {
	r := start(t, nil)
	// A second agent on a live socket must not start.
	err := Run(context.Background(), Config{Socket: r.sock, Load: func() (peers.Env, bool, error) { return peers.Env{}, true, nil }})
	if err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("second agent: %v", err)
	}
	if rep, err := Call(r.sock, Request{Op: OpState}, time.Second); err != nil || rep.State == nil {
		t.Errorf("first agent damaged: %v", err)
	}
	r.stop(t)
	// A dead socket file is replaced.
	if err := os.WriteFile(r.sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := listen(r.sock)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	ln.Close()
}

func TestTickPrunesSessionFiles(t *testing.T) {
	var state string
	r := start(t, func(c *Config) {
		base := filepath.Dir(filepath.Dir(filepath.Dir(c.Socket)))
		state = filepath.Join(base, "state")
		old := filepath.Join(peers.SessionsDir(state), "old.json")
		os.MkdirAll(filepath.Dir(old), 0o700)
		os.WriteFile(old, []byte("{}"), 0o600)
		when := time.Now().Add(-8 * 24 * time.Hour)
		os.Chtimes(old, when, when)
	})
	defer r.stop(t)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(filepath.Join(peers.SessionsDir(state), "old.json")); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("old session file not pruned")
}

func TestRotatingLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logs", "agent.log")
	l, err := newRotatingLog(p, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 20; i++ {
		l.Write([]byte(strings.Repeat("x", 40) + "\n"))
	}
	for _, f := range []string{p, p + ".1", p + ".2", p + ".3"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if _, err := os.Stat(p + ".4"); err == nil {
		t.Error("kept more than 3")
	}
}

func TestSocketPath(t *testing.T) {
	if got := SocketPath("linux", "/run/user/7", "/s"); got != "/run/user/7/sy/agent.sock" {
		t.Error(got)
	}
	if got := SocketPath("darwin", "", "/s"); got != "/s/agent/agent.sock" {
		t.Error(got)
	}
}

func TestSocketPathLengthRule(t *testing.T) {
	long := "/very/" + strings.Repeat("deep/", 30)
	for _, goos := range []string{"darwin", "linux"} {
		max := MaxSocketPath(goos)
		if p := socketPath(goos, "/run/user/7", "/s", "/tmp", 7); len(p) > max {
			t.Errorf("%s short: %d bytes", goos, len(p))
		}
		// Long inputs fall back to <tmp>/sy-<uid>/agent.sock.
		if p := socketPath(goos, long, long, "/var/folders/ab/cd/T", 501); p != "/var/folders/ab/cd/T/sy-501/agent.sock" {
			t.Errorf("%s long: %q", goos, p)
		}
		// A long temp dir falls back to /tmp.
		if p := socketPath(goos, long, long, long, 501); p != "/tmp/sy-501/agent.sock" || len(p) > max {
			t.Errorf("%s long tmp: %q", goos, p)
		}
	}
	// The boundary: exactly 103 bytes is kept, one more is not.
	state := "/" + strings.Repeat("a", 103-len("//agent/agent.sock"))
	if p := socketPath("darwin", "", state, "/t", 1); p != state+"/agent/agent.sock" || len(p) != 103 {
		t.Errorf("boundary: %d %q", len(p), p)
	}
	if p := socketPath("darwin", "", state+"b", "/t", 1); p != "/t/sy-1/agent.sock" {
		t.Errorf("over boundary: %q", p)
	}
}

// "Hooks never wait on it": nothing in the hook path may import the agent.
func TestHookDoesNotImportAgent(t *testing.T) {
	var files []string
	for _, pat := range []string{"../hook/*.go", "../statusline/*.go", "../../cmd/sy/cmd_hook.go", "../../cmd/sy/tick.go"} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	if len(files) < 3 {
		t.Fatalf("found only %v", files)
	}
	for _, f := range files {
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range af.Imports {
			if strings.Contains(im.Path.Value, "switchyard/internal/agent") {
				t.Errorf("%s imports the agent", f)
			}
		}
	}
}

// A socket another user listens on (a shared /tmp fallback taken first) gets nothing.
func TestCallRefusesAnotherUsersSocket(t *testing.T) {
	base, err := os.MkdirTemp("", "sya")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	sock := filepath.Join(base, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan int, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		b := make([]byte, 64)
		n, _ := c.Read(b)
		got <- n
	}()
	old := ownUID
	t.Cleanup(func() { ownUID = old })
	ownUID = func() int { return os.Getuid() + 1 }
	_, err = Call(sock, Request{Op: OpSend, Text: "the secret text"}, time.Second)
	if !errors.Is(err, ErrNoAgent) {
		t.Fatalf("Call: %v", err)
	}
	if n := <-got; n != 0 {
		t.Errorf("another user's listener read %d bytes", n)
	}
}

// brokenListener fails every Accept with an error that is not a timeout (EMFILE, say).
type brokenListener struct{ net.Listener }

func (b brokenListener) Accept() (net.Conn, error) {
	return nil, errors.New("accept: too many open files")
}

func TestAcceptErrorEndsRun(t *testing.T) {
	old := listenSocket
	t.Cleanup(func() { listenSocket = old })
	listenSocket = func(p string) (net.Listener, error) {
		ln, err := listen(p)
		return brokenListener{ln}, err
	}
	base, err := os.MkdirTemp("", "sya")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Config{Socket: filepath.Join(base, "sy", "agent.sock"), Tick: time.Hour,
			Load: func() (peers.Env, bool, error) { return peers.Env{State: base}, true, nil }})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "too many open files") {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run hung after an accept error (its goroutines were never told to stop)")
	}
}
