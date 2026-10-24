package peers_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/peers"
)

// A `claude -p` started from inside a session (its Bash tool runs it) inherits the parent's
// environment, CLAUDE_CONFIG_DIR and CLAUDECODE included, and may have no registry file of
// its own. The walk must stop at it: sending as the parent session above it would put the
// child's words in the parent's name.
//
// world: claude (500, sess-a, registered) -> bash (501) -> claude -p (600) -> bash (601) -> wy
func nestedWorld(t *testing.T) (*fx, peers.Env) {
	f, slot := whoamiWorld(t)
	env := []string{"HOME=" + f.home, "CLAUDECODE=1", "CLAUDE_CONFIG_DIR=" + slot, "CLAUDE_CODE_SESSION_ID=sess-a"}
	f.proc(600, 501, "claude", "/home/u/.local/share/claude/versions/2.1.300", "claude -p do-the-thing", 5000, env...)
	f.proc(601, 600, "bash", "/usr/bin/bash", "bash -c eval", 5001, env...)
	e := f.env()
	e.StartPID = 601
	return f, e
}

func TestWhoamiNestedPrintSessionIsNotTheParent(t *testing.T) {
	_, e := nestedWorld(t)
	c, rf := e.Whoami()
	if rf == nil {
		t.Fatalf("a nested claude -p was attributed to %q (pid %d), the session above it", c.Entry.SessionID, c.ClaudePID)
	}
	if rf.Code != peers.CodeNotASession || !strings.Contains(rf.Detail, "pid 600") {
		t.Errorf("refusal = %+v", rf)
	}
}

func TestWhoamiNestedPrintSessionWithItsOwnRegistry(t *testing.T) {
	f, e := nestedWorld(t)
	f.registry(filepath.Join(f.home, ".claude-2"), 600, "sess-child", "child", f.home, "", 5000)
	c, rf := e.Whoami()
	if rf != nil || c.ClaudePID != 600 || c.Entry.SessionID != "sess-child" {
		t.Fatalf("%v %+v", rf, c)
	}
}

func TestWhoamiNestedNpmClaudeKnownFromItsHooks(t *testing.T) {
	// An npm install runs under node through a symlink named claude: by its executable it is
	// only a name, but a session file written by the child's own hooks names its pid.
	f, slot := whoamiWorld(t)
	f.proc(600, 501, "claude", "/usr/bin/node", "node /usr/local/bin/claude -p x", 5000, "CLAUDE_CONFIG_DIR="+slot)
	f.proc(601, 600, "bash", "/usr/bin/bash", "bash", 5001)
	e := f.env()
	e.StartPID = 601
	if c, rf := e.Whoami(); rf != nil || c.Entry.SessionID != "sess-a" {
		t.Fatalf("without evidence a name-only hit is passed over: %v %+v", rf, c)
	}
	f.write(filepath.Join(peers.SessionsDir(f.state), "sess-child.json"),
		mustJSON(t, peers.SessionFile{V: 1, Session: "sess-child", PID: 600, ConfigDir: "~/.claude-2"}))
	if c, rf := e.Whoami(); rf == nil {
		t.Fatalf("the child's own session file names pid 600, yet it was sent as %q", c.Entry.SessionID)
	}
}

func TestWhoamiPassesOverAClaudeNamedWrapper(t *testing.T) {
	// A process below the session that is only named claude (a shell script) does not block
	// the walk.
	f, _ := whoamiWorld(t)
	f.proc(650, 501, "claude", "/usr/bin/bash", "/bin/bash /home/u/bin/claude", 6000)
	f.proc(651, 650, "wy", "/usr/local/bin/wy", "wy peers send", 6001)
	e := f.env()
	e.StartPID = 651
	if c, rf := e.Whoami(); rf != nil || c.Entry.SessionID != "sess-a" {
		t.Fatalf("%v %+v", rf, c)
	}
}

func TestIsClaudeProgram(t *testing.T) {
	yes := []peers.ProcInfo{
		{Exe: "/home/u/.local/share/claude/versions/2.1.300"}, {Exe: "/usr/bin/claude"}, {Exe: "/x/claude.exe"},
		{Exe: "/usr/bin/node", Args: "node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js -p hi"},
	}
	no := []peers.ProcInfo{
		{Exe: "/usr/bin/bash", Comm: "claude", Args: "/bin/bash /home/u/bin/claude"}, {Comm: "claude"}, {Argv0: "claude"},
		{Exe: "/usr/bin/node", Args: "node /usr/local/bin/claude -p x"},
	}
	for _, p := range yes {
		if !peers.IsClaudeProgram(p) {
			t.Errorf("%+v should be the Claude Code program", p)
		}
	}
	for _, p := range no {
		if peers.IsClaudeProgram(p) {
			t.Errorf("%+v is only named claude", p)
		}
	}
}
