package sshx_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/sshx"
	"github.com/mraj602/switchyard/internal/sshx/sshxtest"
)

func TestMain(m *testing.M) {
	sshxtest.Main()
	os.Exit(m.Run())
}

func TestForbiddenOptionsRefused(t *testing.T) {
	for _, o := range []string{
		"StrictHostKeyChecking=no", "stricthostkeychecking=accept-new", "StrictHostKeyChecking no",
		"UserKnownHostsFile=/dev/null", "GlobalKnownHostsFile=/dev/null", "CheckHostIP=no",
		"StrictHostKeyChecking=\"no\"",
	} {
		if _, err := sshx.ParseOption(o); !errors.Is(err, sshx.ErrForbidden) {
			t.Errorf("--ssh-option %q: %v, want ErrForbidden", o, err)
		}
	}
	for _, o := range []string{"Port=2222", "StrictHostKeyChecking=yes", "IdentityFile=~/.ssh/id_x", "ProxyJump=bastion"} {
		if _, err := sshx.ParseOption(o); err != nil {
			t.Errorf("--ssh-option %q refused: %v", o, err)
		}
	}
	for _, o := range []string{"", "=x", "Bad-Key=1", "Port=1\nX=2"} {
		if _, err := sshx.ParseOption(o); err == nil {
			t.Errorf("--ssh-option %q accepted", o)
		}
	}
	for _, cmd := range []string{
		"ssh -o StrictHostKeyChecking=no", "ssh -oStrictHostKeyChecking=accept-new",
		"ssh -F cfg -o UserKnownHostsFile=/dev/null", "ssh -o StrictHostKeyChecking no",
	} {
		env := func(string) string { return cmd }
		if _, err := sshx.FromEnv(env); !errors.Is(err, sshx.ErrForbidden) {
			t.Errorf("SY_SSH_COMMAND=%q: %v, want ErrForbidden", cmd, err)
		}
	}
	r, err := sshx.FromEnv(func(string) string { return "ssh -F /tmp/cfg -o StrictHostKeyChecking=yes" })
	if err != nil || len(r.Program) != 5 {
		t.Fatalf("FromEnv: %v %v", r, err)
	}
	if _, err := r.Argv(sshx.Call{Host: "h", Extra: []string{"-o", "StrictHostKeyChecking=no"}}); err == nil {
		t.Error("Extra with StrictHostKeyChecking=no accepted")
	}
}

func TestArgvShape(t *testing.T) {
	r, _ := sshx.New(nil, "Port=2222")
	r.Batch = true
	r.ApplyDefaults(sshx.ParseConfig([]byte("connecttimeout none\nserveraliveinterval 0\n")))
	argv, err := r.Argv(sshx.Call{Host: "me@gpu-box", Remote: []string{".local/bin/sy", "version", "--short"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "ssh -o Port=2222 -o ConnectTimeout=15 -o ServerAliveInterval=15 -o BatchMode=yes -T me@gpu-box -- '.local/bin/sy' 'version' '--short'"
	if got := strings.Join(argv, " "); got != want {
		t.Fatalf("argv\n got %s\nwant %s", got, want)
	}
	r.ApplyDefaults(sshx.ParseConfig([]byte("connecttimeout 5\nserveraliveinterval 30\n")))
	if len(r.Defaults) != 0 {
		t.Fatalf("defaults over the person's config: %v", r.Defaults)
	}
	tty, _ := r.Argv(sshx.Call{Host: "gpu-box", Remote: []string{".local/bin/sy", "account", "login", "~/.claude-2"}, TTY: true})
	if !contains(tty, "-t") || contains(tty, "BatchMode=yes") {
		t.Fatalf("tty argv %q", tty)
	}
	for _, h := range []string{"-oProxyCommand=x", "host:22", "a b", ""} {
		if _, err := r.Argv(sshx.Call{Host: h}); err == nil {
			t.Errorf("host %q accepted", h)
		}
	}
	for _, w := range []string{"a b", "x'y", "$(id)", "a;b", "~me/x y"} {
		if _, err := r.Argv(sshx.Call{Host: "h", Remote: []string{"echo", w}}); err == nil {
			t.Errorf("remote word %q accepted", w)
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestScriptLint(t *testing.T) {
	for _, s := range []string{"echo 'x'", "echo hi!", "echo `id`", "printf a\\n", "a\nb", ""} {
		if err := sshx.LintScript(s); err == nil {
			t.Errorf("script %q passed the lint", s)
		}
	}
	w, err := sshx.Script(`echo "$1"; echo $#`, "a=b")
	if err != nil || len(w) != 5 || w[0] != "sh" || w[3] != "sy-remote" {
		t.Fatalf("Script: %q %v", w, err)
	}
	if _, err := sshx.Script("echo", "a b"); err == nil {
		t.Error("script arg with a space accepted")
	}
}

// The quoted command line must reach the program as the same words through every login
// shell sy supports (machines.md 4.3). Shells that are not installed are skipped.
func TestQuotingSurvivesShells(t *testing.T) {
	script, err := sshx.Script(`for a in "$0" "$@"; do printf "[%s]" "$a"; done; d=$HOME; test -n "$d" && printf "|"`, "--hub-url", "https://hub.ts.net:443/x", "a=b,c", "~/.claude-2", "50%", "m_01ABC@x+y")
	if err != nil {
		t.Fatal(err)
	}
	line := sshx.QuoteAll(script)
	want := "[sy-remote][--hub-url][https://hub.ts.net:443/x][a=b,c][~/.claude-2][50%][m_01ABC@x+y]|"
	ran := 0
	for _, sh := range []string{"bash", "dash", "zsh", "fish", "sh"} {
		p, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("%s not installed, skipped", sh)
			continue
		}
		ran++
		out, err := exec.Command(p, "-c", line).CombinedOutput()
		if err != nil || string(out) != want {
			t.Errorf("%s: %q %v, want %q", sh, out, err, want)
		}
	}
	if ran == 0 {
		t.Skip("no shell installed")
	}
}

func TestParseConfigAndForwards(t *testing.T) {
	c := sshx.ParseConfig([]byte("Host gpu-box\nhostname gpu-box.tail.ts.net\nuser me\nport 2222\n" +
		"remoteforward [127.0.0.1]:47830 [127.0.0.1]:8787\nremoteforward 127.0.0.1:47831 hub.ts.net:443\n" +
		"proxyjump none\nstricthostkeychecking no\n"))
	if c.HostName() != "gpu-box.tail.ts.net" || c.User() != "me" || c.Port() != 2222 || c.ProxyJump() != "" {
		t.Fatalf("config %v", c)
	}
	fw := c.RemoteForwards()
	if len(fw) != 2 || fw[0] != "127.0.0.1:47830 127.0.0.1:8787" || fw[1] != "127.0.0.1:47831 hub.ts.net:443" {
		t.Fatalf("forwards %q", fw)
	}
	if !c.HostKeyCheckingOff() {
		t.Fatal("checking off not seen")
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		stderr string
		want   sshx.Class
	}{
		{"No ED25519 host key is known for gpu-box and you have requested strict checking.\nHost key verification failed.\n", sshx.ClassUnknownHostKey},
		{"@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\nHost key verification failed.\n", sshx.ClassChangedHostKey},
		{"me@gpu-box: Permission denied (publickey).\n", sshx.ClassAuth},
		{"ssh: connect to host gpu-box port 22: Connection refused\n", sshx.ClassConnect},
		{"ssh: Could not resolve hostname nope: Name or service not known\n", sshx.ClassConnect},
		{"Error: remote port forwarding failed for listen port 47830\n", sshx.ClassForward},
		{"something else\n", sshx.ClassOther},
	} {
		e := sshx.Classify("gpu-box", 255, tc.stderr)
		if e.Class != tc.want {
			t.Errorf("%q: class %s, want %s", tc.stderr, e.Class, tc.want)
		}
		if e.Error() == "" {
			t.Error("empty message")
		}
	}
}

func TestRunThroughFakeSSH(t *testing.T) {
	f := sshxtest.New(t, "-F", "/dev/null/test-config")
	f.RemoteHome("gpu-box")
	r, err := sshx.FromEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "Q2xhdWRlIGlzIG5vdCBoZXJl"
	script, _ := sshx.Script(`read s; printf "%s|%s" "$1" "$s" | wc -c`, "arg")
	res, err := r.Run(context.Background(), sshx.Call{Host: "me@gpu-box", Remote: script, Stdin: strings.NewReader(secret + "\n")})
	if err != nil || res.Exit != 0 || strings.TrimSpace(string(res.Stdout)) != "28" {
		t.Fatalf("run: %q %q %v", res.Stdout, res.Stderr, err)
	}
	f.AssertNoSecret(secret)
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Stdin != secret+"\n" || calls[0].Host != "me@gpu-box" || !contains(calls[0].Argv, "/dev/null/test-config") {
		t.Fatalf("calls %+v", calls)
	}

	// A nonzero remote exit is not an error; exit 255 is classified.
	res, err = r.Run(context.Background(), sshx.Call{Host: "gpu-box", Remote: []string{"sh", "-c", "exit 7"}})
	if err != nil || res.Exit != 7 {
		t.Fatalf("exit 7: %v %+v", err, res)
	}
	f.Rules(sshxtest.Rule{Match: "", Host: "bad", Stderr: "bad: Permission denied (publickey).\n", Exit: 255})
	_, err = r.Run(context.Background(), sshx.Call{Host: "bad", Remote: []string{"true"}})
	var se *sshx.Error
	if !errors.As(err, &se) || se.Class != sshx.ClassAuth {
		t.Fatalf("auth failure: %v", err)
	}

	// TTY mode passes the person's terminal through.
	var out bytes.Buffer
	r.Stdin, r.Stdout, r.Stderr = strings.NewReader("123-456\n"), &out, &out
	code, err := r.RunTTY(context.Background(), sshx.Call{Host: "gpu-box", Remote: []string{"sh", "-c", "read c; echo got-$c"}})
	if err != nil || code != 0 || !strings.Contains(out.String(), "got-123-456") {
		t.Fatalf("tty: %d %v %q", code, err, out.String())
	}
	last := f.Calls()[len(f.Calls())-1]
	if !last.TTY {
		t.Fatal("no -t")
	}

	cfg, err := r.G(context.Background(), "me@gpu-box")
	if err != nil || cfg.User() != "me" || cfg.HostName() != "gpu-box" {
		t.Fatalf("G: %v %v", cfg, err)
	}
}
