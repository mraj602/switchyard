package terminal

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A pseudo-terminal pair, made by hand (no `script`, no pty library): the master is ours,
// the slave becomes the controlling terminal of the fake "session".
func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("ptsname: %w", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

// session is a fake Claude Code session on a pty: a shell script that puts the terminal in
// raw mode, starts the pane's child (`sy board --follow --ndjson --session <id>`, sharing
// the pty as its stdin and controlling terminal) and then logs every input byte the
// terminal delivers to <dir>/input.log.
type session struct {
	t      *testing.T
	dir    string
	master *os.File
	cmd    *exec.Cmd
}

func startSession(t *testing.T, m *machine, dir, sid string) *session {
	t.Helper()
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal on this host: %v", err)
	}
	script := filepath.Join(dir, "session.sh")
	writeFile(t, script, `#!/bin/sh
SY="$1"; DIR="$2"; SID="$3"
stty raw -echo 2>/dev/null
"$SY" board --follow --ndjson --session "$SID" <&0 >"$DIR/board.out" 2>"$DIR/board.err" &
echo $! > "$DIR/board.pid"
: > "$DIR/ready"
exec cat > "$DIR/input.log"
`, 0o755)
	cmd := exec.Command("/bin/sh", script, syBin, dir, sid)
	cmd.Env = m.envFor()
	cmd.Dir = m.home
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		t.Fatal(err)
	}
	slave.Close()
	// Whatever the session prints goes nowhere, but must be drained.
	go func() { _, _ = io.Copy(io.Discard, master) }()
	s := &session{t: t, dir: dir, master: master, cmd: cmd}
	t.Cleanup(s.stop)
	return s
}

func (s *session) stop() {
	if s.cmd == nil {
		return
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL) // the session, and the board it started
	_, _ = s.cmd.Process.Wait()
	s.master.Close()
	s.cmd = nil
}

// alive reports whether the session's shell (now `cat`) still runs.
func (s *session) alive() bool { return syscall.Kill(s.cmd.Process.Pid, 0) == nil }

// boardPID is the pid of the board child, once started.
func (s *session) boardPID() string {
	return strings.TrimSpace(readFile(filepath.Join(s.dir, "board.pid")))
}

// wait polls for a condition.
func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
