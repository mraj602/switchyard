package keys

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeProc is one process of a fake /proc tree.
type fakeProc struct {
	pid, ppid int
	comm      string
	exe       string // symlink target; "" = none
	cmdline   []string
	environ   []string // "K=V"
	noStat    bool
	noEnviron bool // environ unreadable (mode 0)
}

func writeProc(t *testing.T, root string, p fakeProc) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(p.pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !p.noStat {
		// The comm in stat may hold spaces and ')': the reader must use the last ')'.
		stat := fmt.Sprintf("%d (%s) S %d %d 0 0 -1 4194560", p.pid, p.comm+") x", p.ppid, p.pid)
		os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "comm"), []byte(p.comm+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(p.cmdline, "\x00")+"\x00"), 0o644)
	if p.exe != "" {
		os.Symlink(p.exe, filepath.Join(dir, "exe"))
	}
	env := filepath.Join(dir, "environ")
	os.WriteFile(env, []byte(strings.Join(p.environ, "\x00")+"\x00"), 0o644)
	if p.noEnviron {
		os.Chmod(env, 0)
	}
}

func fakeTree(t *testing.T, procs ...fakeProc) string {
	root := t.TempDir()
	for _, p := range procs {
		writeProc(t, root, p)
	}
	return root
}

func guardFor(root string, ppid int, tty bool) Guard {
	return Guard{
		GOOS:    "linux",
		Getppid: func() int { return ppid },
		HasEnv:  func(string) bool { return false },
		Procs:   LinuxProcs{Root: root},
		OpenTTY: func() error {
			if tty {
				return nil
			}
			return errors.New("no tty")
		},
	}
}

var shell = fakeProc{comm: "bash", exe: "/usr/bin/bash", cmdline: []string{"-bash"}, environ: []string{"HOME=/home/u", "TERM=xterm"}}

func at(p fakeProc, pid, ppid int) fakeProc { p.pid, p.ppid = pid, ppid; return p }

func TestGuardAncestry(t *testing.T) {
	claude := fakeProc{comm: "claude", exe: "/home/u/.local/share/claude/versions/2.1.300", cmdline: []string{"claude"}}
	versionsOnly := fakeProc{comm: "2.1.299", exe: "/home/u/.local/share/claude/versions/2.1.299", cmdline: []string{"/home/u/.local/share/claude/versions/2.1.299"}}
	npm := fakeProc{comm: "node", exe: "/usr/bin/node", cmdline: []string{"node"}, environ: []string{}}
	npm.exe = "/usr/lib/node_modules/@anthropic-ai/claude-code/bin/node"
	renamed := fakeProc{comm: "x", exe: "/opt/x", cmdline: []string{"x"}, environ: []string{"CLAUDECODE=1", "CLAUDE_CODE_MESSAGING_TOKEN=hunter2"}}
	argv0 := fakeProc{comm: "MainThread", exe: "/opt/node", cmdline: []string{"/usr/local/bin/claude", "--resume"}}

	cases := []struct {
		name    string
		procs   []fakeProc
		tty     bool
		wantPID int
		reason  string // a refusal for another reason
	}{
		{"claude parent", []fakeProc{at(claude, 100, 1)}, true, 100, ""},
		{"claude grandparent", []fakeProc{at(shell, 200, 100), at(claude, 100, 1)}, true, 100, ""},
		{"versions executable", []fakeProc{at(shell, 200, 100), at(versionsOnly, 100, 1)}, true, 100, ""},
		{"npm layout", []fakeProc{at(npm, 100, 1)}, true, 100, ""},
		{"argv0 claude", []fakeProc{at(argv0, 100, 1)}, true, 100, ""},
		{"CLAUDECODE in an ancestor's environ", []fakeProc{at(shell, 200, 100), at(renamed, 100, 1)}, true, 100, ""},
		{"reparented, terminal: allowed", []fakeProc{at(shell, 200, 1)}, true, 0, ""},
		{"reparented, no terminal: refused", []fakeProc{at(shell, 200, 1)}, false, 0, "terminal"},
		{"unreadable stat stops the walk", []fakeProc{at(shell, 200, 100), func() fakeProc { p := at(claude, 100, 1); p.noStat = true; return p }()}, true, 0, ""},
		{"unreadable environ is skipped", []fakeProc{func() fakeProc { p := at(shell, 200, 100); p.noEnviron = true; return p }(), at(claude, 100, 1)}, true, 100, ""},
	}
	if os.Getuid() == 0 {
		cases = slices.DeleteFunc(cases, func(c struct {
			name    string
			procs   []fakeProc
			tty     bool
			wantPID int
			reason  string
		}) bool {
			return strings.Contains(c.name, "unreadable environ")
		})
	}
	for _, c := range cases {
		root := fakeTree(t, c.procs...)
		g := guardFor(root, c.procs[0].pid, c.tty)
		err := g.Check()
		var ge *GuardError
		switch {
		case c.wantPID != 0:
			if !errors.As(err, &ge) || ge.PID != c.wantPID {
				t.Errorf("%s: %v, want a refusal at pid %d", c.name, err, c.wantPID)
			} else if !strings.Contains(err.Error(), "inside a Claude Code session (pid "+strconv.Itoa(c.wantPID)) {
				t.Errorf("%s: message %q", c.name, err)
			}
		case c.reason != "":
			if !errors.As(err, &ge) || !strings.Contains(ge.Reason, c.reason) || !errors.Is(err, ErrGuard) {
				t.Errorf("%s: %v", c.name, err)
			}
		default:
			if err != nil {
				t.Errorf("%s: refused: %v", c.name, err)
			}
		}
	}
}

func TestGuardStepCap(t *testing.T) {
	// A chain of 70 shells, claude at the top: the walk stops after 64 steps without a hit.
	var procs []fakeProc
	for i := range 70 {
		procs = append(procs, at(shell, 1000+i, 1000+i+1))
	}
	procs = append(procs, at(fakeProc{comm: "claude", exe: "/usr/bin/claude"}, 1070, 1))
	root := fakeTree(t, procs...)
	if _, _, ok := guardFor(root, 1000, true).ClaudeAncestor(); ok {
		t.Fatal("walked past 64 steps")
	}
	// Within the cap it is found.
	if pid, _, ok := guardFor(root, 1010, true).ClaudeAncestor(); !ok || pid != 1070 {
		t.Fatalf("within the cap: %d %v", pid, ok)
	}
}

func TestGuardEnvironmentNamesOnly(t *testing.T) {
	// Step 1 asks for names only, and only Claude Code's three.
	var asked []string
	g := guardFor(t.TempDir(), 1, true)
	g.HasEnv = func(n string) bool { asked = append(asked, n); return n == "CLAUDE_CODE_ENTRYPOINT" }
	err := g.Check()
	var ge *GuardError
	if !errors.As(err, &ge) || !strings.Contains(err.Error(), "CLAUDE_CODE_ENTRYPOINT") {
		t.Fatalf("env refusal: %v", err)
	}
	for _, n := range asked {
		if !slices.Contains(claudeEnv, n) {
			t.Fatalf("asked for %q", n)
		}
	}
	// The environ reader returns names, never values.
	names, err := EnvNames(strings.NewReader("CLAUDECODE=1\x00CLAUDE_CODE_MESSAGING_TOKEN=hunter2\x00EMPTY=\x00NOEQ\x00\x00LAST=x"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"CLAUDECODE", "CLAUDE_CODE_MESSAGING_TOKEN", "EMPTY", "NOEQ", "LAST"}; !slices.Equal(names, want) {
		t.Fatalf("names %q", names)
	}
	// And the /proc reader keeps no value either.
	root := fakeTree(t, at(fakeProc{comm: "x", environ: []string{"CLAUDE_CODE_MESSAGING_TOKEN=hunter2", "A=b"}}, 5, 1))
	p, err := LinuxProcs{Root: root}.Read(5)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", p), "hunter2") {
		t.Fatal("a value reached the process record")
	}
	if p.PPid != 1 || p.Comm != "x" {
		t.Fatalf("read %+v", p)
	}
}

func TestGuardMacAndWindows(t *testing.T) {
	ps := map[int]string{
		300: "  200 /bin/zsh -zsh",
		200: "  100 /Users/u/.local/bin/claude claude --resume",
		100: "    1 /sbin/launchd /sbin/launchd",
	}
	var calls []int
	g := Guard{GOOS: "darwin", Getppid: func() int { return 300 }, HasEnv: func(string) bool { return false },
		Procs: PSProcs{Run: func(pid int) (string, error) {
			calls = append(calls, pid)
			if s, ok := ps[pid]; ok {
				return s, nil
			}
			return "", errors.New("exit 1")
		}},
		OpenTTY: func() error { return nil }}
	var ge *GuardError
	if err := g.Check(); !errors.As(err, &ge) || ge.PID != 200 {
		t.Fatalf("mac: %v (calls %v)", err, calls)
	}
	ps[200] = "  100 /Applications/iTerm.app/Contents/MacOS/iTerm2 iTerm2"
	if err := g.Check(); err != nil {
		t.Fatalf("mac terminal: %v", err)
	}
	w := Guard{GOOS: "windows", OpenTTY: func() error { return nil }}
	if err := w.Check(); !errors.Is(err, ErrGuard) || !strings.Contains(err.Error(), "Windows") {
		t.Fatalf("windows: %v", err)
	}
}
