package terminal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

const (
	focusLaptop = "warp://session/0123456789abcdef0123456789abcdef"
	focusSSH    = "warp://session/fedcba9876543210fedcba9876543210"
)

// with returns base plus more KEY=VALUE entries as a new slice.
func with(base []string, kv ...string) []string {
	return append(append([]string{}, base...), kv...)
}

// openerFile is where a machine's fake opener records, and what is in it.
func (m *machine) openerFile() string {
	for _, kv := range m.extra {
		if v, ok := strings.CutPrefix(kv, "WY_TEST_OPENER="); ok {
			return v
		}
	}
	m.t.Fatal("no opener file")
	return ""
}

func (m *machine) openerRecords() [][]string { return m.opened(m.openerFile()) }

func sameArgv(got [][]string, want ...[]string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if strings.Join(got[i], "\x00") != strings.Join(want[i], "\x00") {
			return false
		}
	}
	return true
}

// E5: focus. A laptop hook carries WARP_FOCUS_URL; `wy term focus --session <id>` hands the
// opener exactly `xdg-open warp://session/<hex>`. The same session asked from the
// gpu-box's board exits 4 and records nothing.
func TestE5Focus(t *testing.T) {
	f := newFleet(t)
	const sid = "e5000000-0000-4000-8000-000000000001"
	f.l.hook(with(f.l.slot(2), "TERM_PROGRAM=WarpTerminal", "WARP_FOCUS_URL="+focusLaptop, "WARP_TERMINAL_SESSION_UUID=0123456789ABCDEF0123456789ABCDEF"),
		sid, "UserPromptSubmit", map[string]any{"prompt": "go"})

	// The laptop's board says it can focus the row, and the URL is not in the View.
	out := f.l.must("board", "--once", "--json")
	if !strings.Contains(out, `"can_focus": true`) || strings.Contains(out, "warp://") {
		t.Errorf("laptop view: can_focus true expected and no URL in it:\n%s", out)
	}
	if v := f.l.boardView(); !v.Rows[sid].CanFocus {
		t.Errorf("laptop row can_focus = false")
	}

	f.l.must("term", "focus", "--session", sid)
	if got := f.l.openerRecords(); !sameArgv(got, openerArgv(focusLaptop)) {
		t.Fatalf("opener got %v, want exactly %v", got, openerArgv(focusLaptop))
	}

	// The gpu-box's board has the row, cannot focus it, and focus exits 4 without opening.
	var gv boardView
	deadline := time.Now().Add(5 * time.Second)
	for {
		gv = f.gpu.boardView()
		if _, ok := gv.Rows[sid]; ok || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if r, ok := gv.Rows[sid]; !ok || r.CanFocus || r.Here {
		t.Errorf("gpu-box row of a laptop session: %+v (present %v)", r, ok)
	}
	if r := f.gpu.wy("term", "focus", "--session", sid); r.code != 4 {
		t.Errorf("gpu-box focus: exit %d, want 4\n%s%s", r.code, r.out, r.err)
	}
	if got := f.gpu.openerRecords(); len(got) != 0 {
		t.Errorf("gpu-box opener recorded %v", got)
	}

	// With no local record the laptop asks the hub (its own machine check applies) and the
	// answer is the same URL.
	if err := os.RemoveAll(filepath.Join(f.l.stateDir(), "outbox")); err != nil {
		t.Fatal(err)
	}
	f.l.must("term", "focus", "--session", sid)
	if got := f.l.openerRecords(); !sameArgv(got, openerArgv(focusLaptop), openerArgv(focusLaptop)) {
		t.Errorf("opener after the hub lookup: %v", got)
	}
}

// E6: focus over SSH. A gpu-box hook carries LC_SY_FOCUS (the laptop tab's URL, sent by
// `wy ssh`) and LC_SY_HOST (the laptop's machine id). The laptop's board can focus the row,
// the gpu-box's cannot, and focus on the laptop opens the laptop URL.
func TestE6FocusOverSSH(t *testing.T) {
	f := newFleet(t)
	const sid = "e6000000-0000-4000-8000-000000000001"
	laptopID := f.l.machineID()
	f.gpu.hook(with(f.gpu.slot(3), "LC_SY_FOCUS="+focusSSH, "LC_SY_HOST="+laptopID),
		sid, "Stop", map[string]any{"last_assistant_message": "Trained it.\n\nShould I push the checkpoint?"})

	v := f.l.boardView()
	r, ok := v.Rows[sid]
	if !ok {
		t.Fatalf("laptop board lacks the gpu-box session: %v", v.order())
	}
	if !r.CanFocus || r.Here {
		t.Errorf("laptop board row: can_focus %v here %v; want true / false", r.CanFocus, r.Here)
	}
	if r := f.gpu.boardView().Rows[sid]; r.CanFocus {
		t.Errorf("gpu-box board can focus its own SSH session")
	}

	f.l.must("term", "focus", "--session", sid)
	if got := f.l.openerRecords(); !sameArgv(got, openerArgv(focusSSH)) {
		t.Errorf("laptop opener got %v, want %v", got, openerArgv(focusSSH))
	}
	// The gpu-box has no tab for it: the session's tab is on the laptop.
	if r := f.gpu.wy("term", "focus", "--session", sid); r.code != 4 {
		t.Errorf("gpu-box focus: exit %d, want 4", r.code)
	}
	if got := f.gpu.openerRecords(); len(got) != 0 {
		t.Errorf("gpu-box opener recorded %v", got)
	}
}

// E7: hostile focus URLs. A record whose focus_url is file:///etc/passwd (and one that is
// https://evil…) never becomes focusable: can_focus is false and `wy term focus` exits 4
// with the opener file empty. A valid URL still opens, so the empty file means something.
func TestE7HostileFocus(t *testing.T) {
	f := newFleet(t)
	hostile := map[string]string{
		"e7000000-0000-4000-8000-000000000001": "file:///etc/passwd",
		"e7000000-0000-4000-8000-000000000002": "https://evil.example/session/0123456789abcdef0123456789abcdef",
		"e7000000-0000-4000-8000-000000000003": "warp://session/0123456789abcdef0123456789abcdef%0a--x",
		"e7000000-0000-4000-8000-000000000004": "warp://tab_config/watchyard-work",
	}
	laptopID := f.l.machineID()
	n := 0
	for sid, url := range hostile {
		n++
		if n%2 == 0 {
			// As a hook would carry it.
			f.l.hook(with(f.l.slot(2), "TERM_PROGRAM=WarpTerminal", "WARP_FOCUS_URL="+url), sid, "UserPromptSubmit", map[string]any{"prompt": "go"})
		} else {
			// As a record already in the outbox (and so on the hub) would.
			f.l.viaOutbox(hookRecord(t, laptopID, "~/.claude-2", seatWork, &record.Term{App: "warp", ID: "t1", FocusURL: url}, sid, "UserPromptSubmit", map[string]any{"prompt": "go"}))
		}
	}
	// One more arrives as a gpu-box record that names the laptop as its host.
	const viaGPU = "e7000000-0000-4000-8000-000000000005"
	f.gpu.hook(with(f.gpu.slot(2), "LC_SY_FOCUS=file:///etc/passwd", "LC_SY_HOST="+laptopID), viaGPU, "UserPromptSubmit", map[string]any{"prompt": "go"})
	hostile[viaGPU] = "file:///etc/passwd (gpu-box)"

	v := f.l.boardView()
	for sid, url := range hostile {
		r, ok := v.Rows[sid]
		if !ok {
			t.Errorf("row %s (%s) missing", sid, url)
			continue
		}
		if r.CanFocus {
			t.Errorf("row with focus_url %q has can_focus = true", url)
		}
		res := f.l.wy("term", "focus", "--session", sid)
		if res.code != 4 {
			t.Errorf("focus of %q: exit %d, want 4\n%s%s", url, res.code, res.out, res.err)
		}
	}
	for _, url := range []string{"file:///etc/passwd", "https://evil.example/x", "warp://session/0123456789abcdef0123456789abcdef%0a"} {
		res := f.l.run("", []string{"TERM_PROGRAM=WarpTerminal"}, "term", "focus", "--url", url)
		if res.code != 4 {
			t.Errorf("--url %q: exit %d, want 4", url, res.code)
		}
	}
	// A valid --url outside Warp is refused too (T7: only where Warp runs).
	if res := f.l.wy("term", "focus", "--url", focusLaptop); res.code != 4 {
		t.Errorf("--url outside Warp: exit %d, want 4", res.code)
	}
	if got := f.l.openerRecords(); len(got) != 0 {
		t.Fatalf("the opener was called for a hostile URL: %v", got)
	}
	if _, err := os.Stat(f.l.openerFile()); err == nil {
		if b, _ := os.ReadFile(f.l.openerFile()); len(b) != 0 {
			t.Fatalf("opener file not empty: %q", b)
		}
	}

	// Control: the seam works, a valid URL inside Warp is recorded.
	f.l.run("", []string{"TERM_PROGRAM=WarpTerminal"}, "term", "focus", "--url", focusLaptop)
	if got := f.l.openerRecords(); !sameArgv(got, openerArgv(focusLaptop)) {
		t.Errorf("control: opener got %v", got)
	}
}
