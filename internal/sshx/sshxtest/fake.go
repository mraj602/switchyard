// Package sshxtest is the fake `ssh` program of the sshx and machineadd tests: the test binary itself, started through WY_SSH_COMMAND, records every argv and
// stdin and either replays a scripted answer or runs the remote command line with `sh -c`
// in a fake remote home folder, as a real sshd would hand it to a login shell.
//
// Use: call Main() first thing in TestMain, then New(t) in a test; it sets WY_SSH_COMMAND.
// Only tests import this package.
package sshxtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const marker = "-wy-fake-ssh"

// envDir names the fake's folder in the fake process's environment.
const envDir = "WY_FAKE_SSH_DIR"

// Rule is a scripted answer: the first rule whose Match is a substring of the remote command
// line (and whose Host matches, when set) answers instead of running anything.
type Rule struct {
	Match  string `json:"match"`
	Host   string `json:"host,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Exit   int    `json:"exit"`
	// Once rules answer one call and are then used up.
	Once bool `json:"once,omitempty"`
	// ForwardOnly rules answer only calls with a -R option.
	ForwardOnly bool `json:"forward_only,omitempty"`
}

// Call is one recorded invocation.
type Call struct {
	Argv   []string `json:"argv"`
	Host   string   `json:"host"`
	Remote string   `json:"remote"` // the command line the remote shell gets
	Stdin  string   `json:"stdin"`
	TTY    bool     `json:"tty"`
	G      bool     `json:"g"` // ssh -G
	Fwd    []string `json:"fwd,omitempty"`
}

// Fake is one fake ssh for a test.
type Fake struct {
	Dir string
	t   testing.TB
}

// Main makes the test binary act as ssh when it was started as one. Call it first in
// TestMain.
func Main() {
	if len(os.Args) > 1 && os.Args[1] == marker {
		os.Exit(fakeMain(os.Args[2:]))
	}
}

// New makes a fake in a temp folder and points WY_SSH_COMMAND at it (with extra fixed
// words, as a person's WY_SSH_COMMAND="ssh -F cfg" would have).
func New(t testing.TB, extra ...string) *Fake {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &Fake{Dir: t.TempDir(), t: t}
	for _, d := range []string{"calls", "used"} {
		if err := os.MkdirAll(filepath.Join(f.Dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(envDir, f.Dir)
	t.Setenv("WY_SSH_COMMAND", strings.Join(append([]string{exe, marker}, extra...), " "))
	return f
}

// Command is the WY_SSH_COMMAND value of this fake.
func (f *Fake) Command() []string { return strings.Fields(os.Getenv("WY_SSH_COMMAND")) }

// Rules replaces the scripted answers.
func (f *Fake) Rules(rs ...Rule) {
	f.t.Helper()
	b, _ := json.Marshal(rs)
	if err := os.WriteFile(filepath.Join(f.Dir, "rules.json"), b, 0o600); err != nil {
		f.t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(f.Dir, "used"))
	_ = os.MkdirAll(filepath.Join(f.Dir, "used"), 0o700)
}

// RemoteHome makes (once) and returns the fake remote's home folder for host. Remote
// commands without a matching rule run there with `sh -c`.
func (f *Fake) RemoteHome(host string) string {
	d := filepath.Join(f.Dir, "remote", hostKey(host))
	if err := os.MkdirAll(d, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return d
}

// SetG sets the `ssh -G <host>` output.
func (f *Fake) SetG(host, out string) {
	if err := os.WriteFile(filepath.Join(f.Dir, "G-"+hostKey(host)), []byte(out), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// Calls returns every recorded call in order.
func (f *Fake) Calls() []Call {
	f.t.Helper()
	ents, err := os.ReadDir(filepath.Join(f.Dir, "calls"))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []Call
	for _, e := range ents { // names are zero-padded, so ReadDir order is call order
		b, err := os.ReadFile(filepath.Join(f.Dir, "calls", e.Name()))
		if err != nil {
			continue
		}
		var c Call
		if json.Unmarshal(b, &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// AssertNoSecret fails the test if any recorded argv word contains one of the secrets.
func (f *Fake) AssertNoSecret(secrets ...string) {
	f.t.Helper()
	for _, c := range f.Calls() {
		for _, w := range c.Argv {
			for _, s := range secrets {
				if s != "" && strings.Contains(w, s) {
					f.t.Errorf("secret %q appears in ssh argv: %q", s, c.Argv)
				}
			}
		}
	}
}

func hostKey(h string) string {
	if i := strings.LastIndexByte(h, '@'); i >= 0 {
		h = h[i+1:]
	}
	return strings.NewReplacer("/", "_", ":", "_").Replace(h)
}

// --- the fake process --------------------------------------------------------------------

// optsWithArg are the ssh options that take an argument.
const optsWithArg = "BbcDEeFIiJLlmOoPpRSWw"

func fakeMain(args []string) int {
	dir := os.Getenv(envDir)
	if dir == "" {
		fmt.Fprintln(os.Stderr, "fake ssh: no "+envDir)
		return 255
	}
	var c Call
	c.Argv = append([]string{"ssh"}, args...)
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		for j := 1; j < len(a); j++ {
			ch := a[j]
			if strings.IndexByte(optsWithArg, ch) >= 0 {
				val := a[j+1:]
				if val == "" && i+1 < len(args) {
					i++
					val = args[i]
				}
				if ch == 'R' {
					c.Fwd = append(c.Fwd, val)
				}
				break
			}
			switch ch {
			case 't':
				c.TTY = true
			case 'G':
				c.G = true
			}
		}
	}
	if i < len(args) {
		c.Host = args[i]
		i++
	}
	if i < len(args) && args[i] == "--" {
		i++
	}
	c.Remote = strings.Join(args[i:], " ")
	if !c.G {
		in, _ := io.ReadAll(os.Stdin)
		c.Stdin = string(in)
	}
	record(dir, c)

	if c.G {
		if b, err := os.ReadFile(filepath.Join(dir, "G-"+hostKey(c.Host))); err == nil {
			os.Stdout.Write(b)
			return 0
		}
		h := hostKey(c.Host)
		user := "me"
		if k := strings.LastIndexByte(c.Host, '@'); k >= 0 {
			user = c.Host[:k]
		}
		fmt.Printf("host %s\nhostname %s\nuser %s\nport 22\nconnecttimeout none\nserveraliveinterval 0\nstricthostkeychecking ask\n", h, h, user)
		return 0
	}
	if r, ok := matchRule(dir, c); ok {
		os.Stdout.WriteString(r.Stdout)
		os.Stderr.WriteString(r.Stderr)
		return r.Exit
	}
	home := filepath.Join(dir, "remote", hostKey(c.Host))
	if _, err := os.Stat(home); err != nil {
		fmt.Fprintf(os.Stderr, "ssh: Could not resolve hostname %s: Name or service not known\n", c.Host)
		return 255
	}
	if c.Remote == "" {
		return 0
	}
	cmd := exec.Command("sh", "-c", c.Remote)
	cmd.Dir = home
	cmd.Env = remoteEnv(home)
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "fake ssh:", err)
		return 255
	}
	return 0
}

// remoteEnv is a clean environment for the fake remote: its own HOME, no WY_*/XDG_*/CLAUDE_*
// from the test, so a real wy run there uses the fake remote's folders.
func remoteEnv(home string) []string {
	// A fixed PATH: the person's own tools (a real claude in ~/.local/bin) stay out of sight.
	// A test puts fake remote programs in <remote home>/bin.
	env := []string{"HOME=" + home, "SHELL=/bin/sh", "USER=me", "LANG=C",
		"PATH=" + filepath.Join(home, "bin") + ":/usr/local/bin:/usr/bin:/bin"}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "TMPDIR", strings.HasPrefix(k, "GO"), k == "SYSTEMROOT", k == "WY_NO_SPAWN":
			env = append(env, kv)
		case strings.HasPrefix(k, "WY_FAKE_REMOTE_"):
			env = append(env, kv) // test knobs for fake remote programs
		}
	}
	return env
}

func record(dir string, c Call) {
	b, _ := json.Marshal(c)
	for n := 1; n < 1_000_000; n++ {
		p := filepath.Join(dir, "calls", fmt.Sprintf("%06d.json", n))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			continue
		}
		f.Write(b)
		f.Close()
		return
	}
}

func matchRule(dir string, c Call) (Rule, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "rules.json"))
	if err != nil {
		return Rule{}, false
	}
	var rs []Rule
	if json.Unmarshal(b, &rs) != nil {
		return Rule{}, false
	}
	for i, r := range rs {
		if r.Host != "" && r.Host != c.Host {
			continue
		}
		if r.ForwardOnly && len(c.Fwd) == 0 {
			continue
		}
		if !strings.Contains(c.Remote, r.Match) {
			continue
		}
		if r.Once {
			p := filepath.Join(dir, "used", fmt.Sprint(i))
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				continue
			}
			f.Close()
		}
		return r, true
	}
	return Rule{}, false
}

// Remotes returns the remote command lines of the calls, for assertions.
func Remotes(cs []Call) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Remote)
	}
	return out
}
