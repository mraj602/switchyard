package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// E11: never types. While E2's scenario runs, a pty-backed fake "session" (its own
// controlling terminal, in raw mode, logging every input byte) shares its terminal with the
// board child of the pane (`wy board --follow --ndjson --session <id>`); `wy board --once`,
// `wy term focus` and the live hook all run meanwhile. The session's input log stays empty.
// A control line written to the master afterwards shows the log can see input at all.
func TestE11NeverTypes(t *testing.T) {
	f := newFleet(t)
	f.seed()
	const sid = "e1100000-0000-4000-8000-000000000001"
	dir := t.TempDir()
	sess := startSession(t, f.l.machine, dir, sid)
	waitFor(t, 10*time.Second, "the session to start", func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil })
	waitFor(t, 5*time.Second, "the board child's pid", func() bool { return sess.boardPID() != "" })

	// The board child really shares the session's terminal as its stdin.
	if tty, err := os.Readlink("/proc/" + sess.boardPID() + "/fd/0"); err != nil || !strings.HasPrefix(tty, "/dev/pts/") {
		t.Fatalf("the board child's stdin is %q (%v), want the session's pty", tty, err)
	}
	childView := func() (boardView, bool) {
		var last boardView
		found := false
		for _, ln := range strings.Split(readFile(filepath.Join(dir, "board.out")), "\n") {
			var v boardView
			if json.Unmarshal([]byte(ln), &v) == nil && v.V == 1 {
				last, found = v, true
			}
		}
		return last, found
	}
	waitFor(t, 15*time.Second, "the board child's first live view", func() bool {
		v, ok := childView()
		return ok && v.Hub.State == "live" && len(v.Rows) == 6
	})
	n0, _ := childView()

	// E2's scenario: a Stop that asks a question.
	f.l.hookNoFlush(f.l.slot(2), sid, "UserPromptSubmit", map[string]any{"prompt": "tidy the schema"})
	f.l.hookNoFlush(f.l.slot(2), sid, "Stop", map[string]any{"last_assistant_message": "Dropped the index.\n\nShould I keep the old column?"})
	f.l.flush()
	waitFor(t, 2*time.Second, "the child's line with the asked row", func() bool {
		v, ok := childView()
		r := v.Rows[sid]
		return ok && r.State == "asked" && r.ThisSession && v.Counts.NeedsYou == n0.Counts.NeedsYou+1
	})
	// More of the board's commands while the session sits on its terminal.
	f.l.must("board", "--once", "--json")
	f.l.must("board", "--once")
	f.l.run("", nil, "term", "focus", "--session", sid) // no focus URL: exit 4, nothing opened
	time.Sleep(1500 * time.Millisecond)

	if b, err := os.ReadFile(filepath.Join(dir, "input.log")); err != nil || len(b) != 0 {
		t.Fatalf("the session received input from the board: %q (%v)", b, err)
	}
	if !sess.alive() {
		t.Fatal("the session died")
	}
	if got := f.l.openerRecords(); len(got) != 0 {
		t.Errorf("the opener ran: %v", got)
	}

	// Control: bytes written to the terminal do arrive in the log.
	if _, err := sess.master.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the control bytes in the input log (the harness would see no input)", func() bool {
		return strings.Contains(readFile(filepath.Join(dir, "input.log")), "ping")
	})
}
