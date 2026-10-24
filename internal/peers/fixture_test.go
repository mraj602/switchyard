package peers_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/peers"
)

// fx is a throwaway world: a HOME with config folders, an XDG_RUNTIME_DIR, a state folder
// and a fake /proc. Nothing in it is the user's real environment.
type fx struct {
	t                          *testing.T
	base, home, state, runtime string
	procRoot                   string
	now                        time.Time
}

func newFx(t *testing.T) *fx {
	t.Helper()
	// A short base: Unix socket paths are limited to about 108 bytes.
	base, err := os.MkdirTemp("", "wyp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	f := &fx{t: t, base: base, home: filepath.Join(base, "h"), state: filepath.Join(base, "s"),
		runtime: filepath.Join(base, "r"), procRoot: filepath.Join(base, "p"), now: time.Unix(1_800_000_000, 0)}
	for _, d := range []string{f.home, f.state, filepath.Join(f.runtime, peers.SocksDirName), f.procRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fx) write(path, content string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// slot makes ~/<name> with a login (account uuid) and a sessions folder.
func (f *fx) slot(name, account string) string {
	dir := filepath.Join(f.home, name)
	f.write(filepath.Join(dir, "settings.json"), "{}")
	f.write(filepath.Join(dir, ".claude.json"),
		fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationUuid":"org-1","emailAddress":"%s@example.test"}}`, account, account))
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		f.t.Fatal(err)
	}
	return dir
}

// proc adds a process to the fake /proc. env is the whole environment as KEY=VALUE.
func (f *fx) proc(pid, ppid int, comm, exe, cmdline string, start int, env ...string) {
	d := filepath.Join(f.procRoot, fmt.Sprint(pid))
	zeros := strings.Repeat("0 ", 17)
	f.write(filepath.Join(d, "stat"), fmt.Sprintf("%d (%s) S %d %s%d 0 0\n", pid, comm, ppid, zeros, start))
	f.write(filepath.Join(d, "comm"), comm+"\n")
	f.write(filepath.Join(d, "cmdline"), strings.ReplaceAll(cmdline, " ", "\x00")+"\x00")
	f.write(filepath.Join(d, "environ"), strings.Join(env, "\x00")+"\x00")
	if exe != "" {
		if err := os.Symlink(exe, filepath.Join(d, "exe")); err != nil {
			f.t.Fatal(err)
		}
	}
}

// claudeProc adds a claude process (native installer layout) with its config folder.
func (f *fx) claudeProc(pid, ppid int, configDir string, start int) {
	env := []string{"HOME=" + f.home, "CLAUDE_CODE_MESSAGING_TOKEN=do-not-read", "PATH=/usr/bin"}
	if configDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	}
	f.proc(pid, ppid, "claude", "/home/u/.local/share/claude/versions/2.1.300", "claude --name x", start, env...)
}

// registry writes <slot>/sessions/<pid>.json.
func (f *fx) registry(slotDir string, pid int, id, name, cwd, sock string, procStart int) {
	f.write(filepath.Join(slotDir, "sessions", fmt.Sprint(pid)+".json"), mustJSON(f.t, map[string]any{
		"pid": pid, "sessionId": id, "cwd": cwd, "procStart": fmt.Sprint(procStart),
		"messagingSocketPath": sock, "name": name, "status": "idle", "kind": "interactive", "peerProtocol": 1,
	}))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *fx) env() peers.Env {
	return peers.Env{
		Home: f.home, State: f.state, Runtime: f.runtime,
		Machine:    identity.Machine{ID: "m_TEST", Name: "laptop"},
		Proc:       peers.LinuxProc{Root: f.procRoot},
		FS:         identity.OSFS{},
		Now:        func() time.Time { return f.now },
		SlotLabels: map[string]string{"~/.claude-2": "work", "~/.claude-5": "personal"},
		NewID: func() func() string {
			n := 0
			return func() string { n++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
		}(),
	}
}

// humanAt writes a session file with a human prompt time and a permission mode.
func (f *fx) sessionFile(id, mode string, humanAt *time.Time) {
	sf := peers.SessionFile{V: 1, Session: id, Mode: mode, HumanAt: humanAt}
	f.write(filepath.Join(peers.SessionsDir(f.state), id+".json"), mustJSON(f.t, sf))
}
