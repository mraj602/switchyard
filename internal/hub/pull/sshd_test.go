package pull_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/pull"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// localSSHD starts a real sshd on a free loopback port with a temp host key. It skips the
// test when sshd or the ssh tools are missing or sshd cannot start as this user.
type localSSHD struct {
	port    int
	hostPub string // "<type> <base64>"
	dir     string
}

func startSSHD(t *testing.T, authorizedKeys func(forced string) string, forced string) *localSSHD {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		for _, p := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd"} {
			if _, e := os.Stat(p); e == nil {
				sshd, err = p, nil
				break
			}
		}
	}
	if err != nil {
		t.Skip("sshd not installed")
	}
	for _, tool := range []string{"ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	hostKey := filepath.Join(dir, "host_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostKey).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v %s", err, out)
	}
	pub, _ := os.ReadFile(hostKey + ".pub")
	f := strings.Fields(string(pub))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(authorizedKeys(forced)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nUsePAM no\n"+
		"PasswordAuthentication no\nKbdInteractiveAuthentication no\nStrictModes no\nLogLevel ERROR\n",
		port, hostKey, filepath.Join(dir, "sshd.pid"), filepath.Join(dir, "authorized_keys"))
	cfgPath := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd := exec.Command(sshd, "-D", "-e", "-f", cfgPath)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("sshd would not start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Skipf("sshd did not listen: %s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &localSSHD{port: port, hostPub: f[0] + " " + f[1], dir: dir}
}

func TestRealSSHDForcedCommandAndRestrict(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	e := newEnv(t)
	ctx := context.Background()
	pub, err := pull.EnsureKey(ctx, e.dir, "testhub", nil)
	if err != nil {
		t.Skipf("no ssh-keygen: %v", err)
	}
	// The forced command stands in for `wy outbox drain --stdio`: it prints what it was asked.
	drain := filepath.Join(t.TempDir(), "drain.sh")
	if err := os.WriteFile(drain, []byte("#!/bin/sh\necho \"FORCED[$SSH_ORIGINAL_COMMAND]\"\nsleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := startSSHD(t, func(forced string) string {
		return fmt.Sprintf("restrict,command=%q %s wy-pull:pt_x\n", forced, pub)
	}, drain)

	tg, err := e.st.AddPullTarget(ctx, store.PullTarget{Name: "local", Host: "127.0.0.1", Port: srv.port, User: u.Username,
		HostKeys: []string{srv.hostPub}})
	if err != nil {
		t.Fatal(err)
	}
	if err := pull.WriteConfig(e.dir, []store.PullTarget{tg}); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(e.dir, "ssh_config")
	alias := pull.Alias(tg)
	run := func(timeout time.Duration, args ...string) (string, string, error) {
		c, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(c, "ssh", append([]string{"-F", cfg}, args...)...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}

	// The documented invocation reaches the forced command.
	out, errs, err := run(10*time.Second, append([]string{"-T", alias}, ".local/bin/wy", "outbox", "drain", "--stdio")...)
	if err != nil {
		t.Skipf("ssh to the local sshd failed (environment): %v %s", err, errs)
	}
	if !strings.Contains(out, "FORCED[") {
		t.Fatalf("forced command did not run: %q", out)
	}
	// Asking for a shell gets the forced command, not a shell.
	out, _, _ = run(10*time.Second, "-T", alias, "sh")
	if !strings.Contains(out, "FORCED[sh]") {
		t.Fatalf("ssh ... sh must run the forced command, got %q", out)
	}
	// Remote forwarding is refused by restrict.
	_, errs, err = run(10*time.Second, "-o", "ClearAllForwardings=no", "-o", "ExitOnForwardFailure=yes", "-R", "0:127.0.0.1:1", "-T", alias, "sh")
	if err == nil {
		t.Fatalf("-R succeeded under restrict: %s", errs)
	}
	// Local forwarding: the listener opens, but the server refuses the channel.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	lport := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	c2, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fwd := exec.CommandContext(c2, "ssh", "-F", cfg, "-o", "ClearAllForwardings=no", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", lport, srv.port), "-T", alias, "sh")
	var fe bytes.Buffer
	fwd.Stderr = &fe
	if err := fwd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fwd.Process.Kill(); _ = fwd.Wait() }()
	var conn net.Conn
	for i := 0; i < 50; i++ {
		if conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", lport)); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn != nil {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _ := conn.Read(make([]byte, 16)) // a refused channel closes the connection: no SSH banner
		conn.Close()
		if n > 0 {
			t.Fatalf("local forwarding passed %d bytes under restrict", n)
		}
	}
	// A terminal is refused: no PTY.
	_, errs, _ = run(10*time.Second, "-tt", alias, "sh")
	if !strings.Contains(errs, "PTY allocation request failed") {
		t.Fatalf("-t must get no PTY, stderr %q", errs)
	}
	// A changed host key stops the connection (StrictHostKeyChecking yes with a pinned key).
	bad := tg
	bad.HostKeys = []string{hostKeyA}
	if err := pull.WriteConfig(e.dir, []store.PullTarget{bad}); err != nil {
		t.Fatal(err)
	}
	_, errs, err = run(10*time.Second, "-T", alias, "sh")
	if err == nil || !strings.Contains(errs, "Host key verification failed") {
		t.Fatalf("a wrong pinned key must fail verification: %v %q", err, errs)
	}
}
