package peers_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/peers"
)

// world: claude (500) -> bash (501, CLAUDECODE set) -> wy (502). The walk starts at 501.
func whoamiWorld(t *testing.T) (*fx, string) {
	f := newFx(t)
	slot := f.slot(".claude-2", "acc-work")
	f.claudeProc(500, 100, slot, 4242)
	f.proc(100, 1, "systemd", "/usr/lib/systemd/systemd", "systemd --user", 1)
	f.proc(501, 500, "bash", "/usr/bin/bash", "bash -c eval", 4300, "CLAUDECODE=1", "CLAUDE_CONFIG_DIR=/wrong/never-used")
	f.proc(502, 501, "wy", "/usr/local/bin/wy", "wy peers send", 4301)
	f.registry(slot, 500, "sess-a", "api", filepath.Join(f.home, "src/api"), filepath.Join(f.runtime, "cc-socks", "500.sock"), 4242)
	return f, slot
}

func TestWhoamiFindsClaudeAndItsFolder(t *testing.T) {
	f, slot := whoamiWorld(t)
	e := f.env()
	e.StartPID = 501
	c, rf := e.Whoami()
	if rf != nil {
		t.Fatal(rf)
	}
	if c.ClaudePID != 500 || c.Entry.SessionID != "sess-a" || c.Slot.Dir != slot {
		t.Errorf("caller = %+v", c)
	}
}

func TestWhoamiUsesClaudeProcessEnvironmentNotCallers(t *testing.T) {
	// The bash process (the caller's own side) says CLAUDE_CONFIG_DIR=/wrong...; only the
	// claude process's value counts.
	f, slot := whoamiWorld(t)
	e := f.env()
	e.StartPID = 502
	c, rf := e.Whoami()
	if rf != nil || c.Slot.Dir != slot {
		t.Fatalf("%v %+v", rf, c)
	}
}

func TestWhoamiDefaultSlotWhenVariableUnset(t *testing.T) {
	f := newFx(t)
	slot := f.slot(".claude", "acc-x")
	f.claudeProc(700, 1, "", 99)
	f.proc(701, 700, "sh", "/bin/sh", "sh", 100)
	f.registry(slot, 700, "sess-d", "d", f.home, filepath.Join(f.runtime, "cc-socks", "700.sock"), 99)
	e := f.env()
	e.StartPID = 701
	c, rf := e.Whoami()
	if rf != nil || !c.Slot.Default {
		t.Fatalf("%v %+v", rf, c.Slot)
	}
}

func TestWhoamiRefusals(t *testing.T) {
	t.Run("no claude above", func(t *testing.T) {
		f := newFx(t)
		f.proc(10, 1, "bash", "/usr/bin/bash", "bash", 1, "CLAUDECODE=1") // CLAUDECODE alone is not claude
		f.proc(11, 10, "wy", "/usr/bin/wy", "wy", 2)
		e := f.env()
		e.StartPID = 10
		if _, rf := e.Whoami(); rf == nil || rf.Code != peers.CodeNotASession || peers.ExitCode(rf.Code) != 4 {
			t.Fatalf("%v", rf)
		}
	})
	t.Run("reparented child has no claude ancestor", func(t *testing.T) {
		f, _ := whoamiWorld(t)
		f.proc(900, 1, "wy", "/usr/bin/wy", "wy peers send", 5) // setsid -f: parent is pid 1
		e := f.env()
		e.StartPID = 900
		if _, rf := e.Whoami(); rf == nil || rf.Code != peers.CodeNotASession {
			t.Fatalf("%v", rf)
		}
	})
	t.Run("unmanaged folder", func(t *testing.T) {
		f := newFx(t)
		other := filepath.Join(f.base, "elsewhere")
		f.write(filepath.Join(other, "sessions", "500.json"), `{"pid":500,"sessionId":"s"}`)
		f.claudeProc(500, 1, other, 1)
		f.proc(501, 500, "sh", "/bin/sh", "sh", 2)
		e := f.env()
		e.StartPID = 501
		if _, rf := e.Whoami(); rf == nil || !strings.Contains(rf.Detail, "not one Watchyard manages") {
			t.Fatalf("%v", rf)
		}
	})
	t.Run("no registry file", func(t *testing.T) {
		f, slot := whoamiWorld(t)
		_ = slot
		f.claudeProc(800, 1, filepath.Join(f.home, ".claude-2"), 7)
		f.proc(801, 800, "sh", "/bin/sh", "sh", 8)
		e := f.env()
		e.StartPID = 801
		if _, rf := e.Whoami(); rf == nil {
			t.Fatal("a claude process with no registry file must not be a session")
		}
	})
	t.Run("recycled pid", func(t *testing.T) {
		f, slot := whoamiWorld(t)
		f.registry(slot, 500, "sess-old", "api", f.home, "", 1111) // start time differs from 4242
		e := f.env()
		e.StartPID = 501
		if _, rf := e.Whoami(); rf == nil {
			t.Fatal("a registry file of another process start must be refused")
		}
	})
	t.Run("unreadable environ", func(t *testing.T) {
		f, _ := whoamiWorld(t)
		if err := os.Remove(filepath.Join(f.procRoot, "500", "environ")); err != nil {
			t.Fatal(err)
		}
		e := f.env()
		e.StartPID = 501
		if _, rf := e.Whoami(); rf == nil {
			t.Fatal("must refuse when the claude environment cannot be read")
		}
	})
}

// macOS cannot read another process's environment: one managed slot holding the pid's
// registry file decides; two refuse.
type noEnvProc struct{ peers.Proc }

func (noEnvProc) ConfigDir(int) (string, bool, error) { return "", false, peers.ErrNoEnv }

func TestWhoamiMacOSMode(t *testing.T) {
	f, slot := whoamiWorld(t)
	e := f.env()
	e.Proc = noEnvProc{peers.LinuxProc{Root: f.procRoot}}
	e.StartPID = 501
	c, rf := e.Whoami()
	if rf != nil || c.Slot.Dir != slot {
		t.Fatalf("%v %+v", rf, c)
	}
	other := f.slot(".claude-5", "acc-p")
	f.registry(other, 500, "sess-dup", "x", f.home, "", 4242)
	if _, rf := e.Whoami(); rf == nil || !strings.Contains(rf.Detail, "more than one") {
		t.Fatalf("two slots with the same pid must refuse: %v", rf)
	}
}

func TestLooksLikeClaude(t *testing.T) {
	yes := []peers.ProcInfo{
		{Exe: "/usr/bin/claude"}, {Argv0: "claude"}, {Comm: "claude"}, {Exe: "/x/claude.exe"},
		{Exe: "/home/u/.local/share/claude/versions/2.1.300", Comm: "2.1.300"},
		{Exe: "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"},
		{Args: "node /opt/bin/claude"}, {Argv0: "/opt/bin/claude"},
	}
	no := []peers.ProcInfo{{Exe: "/usr/bin/bash", Comm: "bash"}, {Comm: "claudette"}, {Args: "vim claude.md"}, {}}
	for _, p := range yes {
		if !peers.LooksLikeClaude(p) {
			t.Errorf("%+v should look like claude", p)
		}
	}
	for _, p := range no {
		if peers.LooksLikeClaude(p) {
			t.Errorf("%+v should not look like claude", p)
		}
	}
}

func TestAncestorsLimitAndCycle(t *testing.T) {
	f := newFx(t)
	for pid := 2; pid <= 100; pid++ {
		f.proc(pid, pid-1, "x", "", "x", pid)
	}
	f.proc(1, 0, "init", "", "init", 1)
	got := peers.Ancestors(peers.LinuxProc{Root: f.procRoot}, 100)
	if len(got) != peers.MaxWalk {
		t.Errorf("walk visited %d, want the cap %d", len(got), peers.MaxWalk)
	}
	f.proc(200, 201, "a", "", "a", 1)
	f.proc(201, 200, "b", "", "b", 1)
	if n := len(peers.Ancestors(peers.LinuxProc{Root: f.procRoot}, 200)); n > peers.MaxWalk {
		t.Errorf("cycle walked %d", n)
	}
}

func TestLinuxProcConfigDirReadsOnlyThatVariable(t *testing.T) {
	f := newFx(t)
	f.proc(5, 1, "claude", "", "claude", 1, "A=1", "CLAUDE_CONFIG_DIR_OTHER=x", "CLAUDE_CONFIG_DIR=/cfg", "B=2")
	v, set, err := peers.LinuxProc{Root: f.procRoot}.ConfigDir(5)
	if err != nil || !set || v != "/cfg" {
		t.Errorf("%q %v %v", v, set, err)
	}
	f.proc(6, 1, "claude", "", "claude", 1, "A=1", "CLAUDE_CONFIG_DIR=")
	if _, set, _ := (peers.LinuxProc{Root: f.procRoot}).ConfigDir(6); set {
		t.Error("an empty value means unset")
	}
	f.proc(7, 1, "claude", "", "claude", 1, "A=1")
	if _, set, err := (peers.LinuxProc{Root: f.procRoot}).ConfigDir(7); set || err != nil {
		t.Error("absent")
	}
}

func TestStatParsingSurvivesOddCommNames(t *testing.T) {
	f := newFx(t)
	zeros := strings.Repeat("0 ", 17)
	f.write(filepath.Join(f.procRoot, "9", "stat"), "9 (a) b) c (d) S 77 "+zeros+"31337 0\n")
	in, err := peers.LinuxProc{Root: f.procRoot}.Info(9)
	if err != nil || in.PPID != 77 {
		t.Fatalf("%+v %v", in, err)
	}
	if st, _ := (peers.LinuxProc{Root: f.procRoot}).StartTime(9); st != "31337" {
		t.Errorf("start %q", st)
	}
}

// recFS records every file the registry reader opens.
type recFS struct {
	identity.FS
	mu     sync.Mutex
	opened []string
}

func (r *recFS) Open(name string) (io.ReadCloser, error) {
	r.mu.Lock()
	r.opened = append(r.opened, name)
	r.mu.Unlock()
	return r.FS.Open(name)
}

func (r *recFS) Stat(name string) (fs.FileInfo, error) {
	r.mu.Lock()
	r.opened = append(r.opened, "stat:"+name)
	r.mu.Unlock()
	return r.FS.Stat(name)
}

func TestRegistryNeverOpensKeyFiles(t *testing.T) {
	f := newFx(t)
	slotDir := f.slot(".claude-2", "acc")
	f.registry(slotDir, 10, "sess-10", "a", f.home, "", 1)
	f.write(filepath.Join(slotDir, "sessions", "10.key"), "SECRET")
	f.write(filepath.Join(slotDir, "sessions", "11.json"), "{torn")
	f.write(filepath.Join(slotDir, "sessions", "notes.json"), "{}")
	f.write(filepath.Join(slotDir, "sessions", "12.json.key"), "SECRET")
	rec := &recFS{FS: identity.OSFS{}}
	slot := identity.Locator{Home: f.home, FS: rec}.SlotFor(slotDir)
	got := peers.ReadRegistry(rec, slot)
	if len(got) != 1 || got[0].SessionID != "sess-10" {
		t.Fatalf("entries: %+v", got)
	}
	for _, o := range rec.opened {
		if strings.HasSuffix(o, ".key") || strings.Contains(o, "credentials") {
			t.Errorf("opened a forbidden file: %s", o)
		}
	}
	// The FS layer itself refuses a .key even when asked.
	if _, err := (identity.OSFS{}).Open(filepath.Join(slotDir, "sessions", "10.key")); !errors.Is(err, identity.ErrForbidden) {
		t.Errorf("OSFS must refuse: %v", err)
	}
}

func TestProcStartMatches(t *testing.T) {
	cases := []struct {
		reg, live string
		want      bool
	}{
		{"4242", "4242", true}, {"4242", "9", false}, {"", "9", true}, {"Mon Oct 12 2026", "9", true}, {"4242", "", true},
	}
	for _, c := range cases {
		if got := peers.ProcStartMatches(c.reg, c.live); got != c.want {
			t.Errorf("%q vs %q = %v", c.reg, c.live, got)
		}
	}
}
