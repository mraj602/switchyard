package terminal

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// E1: the terminal board equals the web board. Three seats on two machines (the gpu-box is
// a second SY_HOME), a plain-text question, a permission and a rate limit; `sy board
// --once --json` has the same row ids, order, status and line2 as /v1/sessions and
// /v1/accounts read with an owner session.
func TestE1BoardEqualsBoard(t *testing.T) {
	f := newFleet(t)
	s := f.seed()

	v := f.waitSame(5*time.Second, func() boardView { return f.l.boardView() })
	if v.V != 1 || v.Source != "hub" || v.Hub.State != "live" {
		t.Fatalf("view: v=%d source=%s hub=%s", v.V, v.Source, v.Hub.State)
	}
	if v.Counts.Sessions != 6 || len(v.Groups) != 3 {
		t.Errorf("counts %+v, %d groups; want 6 sessions in 3 seat groups", v.Counts, len(v.Groups))
	}
	// The seeded kinds really are on the board, in the words the board shows.
	for id, want := range map[string]struct{ state, detail, status, line2 string }{
		s.asked: {"asked", "question", "asked", "Should I keep the old column?"},
		// Known gap: the local source says "perm · Bash" (it reads the PermissionRequest), but
		// /v1/sessions carries no tool, so the hub board says "perm" (terminal.md T2 wants both).
		s.perm:    {"needs_permission", "permission_request", "perm", ""},
		s.done:    {"done", "", "done", "Everything is done: All green."},
		s.limited: {"failed", "rate_limit", "limit", "rate limit"},
	} {
		r, ok := v.Rows[id]
		if !ok {
			t.Errorf("row %s missing", id)
			continue
		}
		if r.State != want.state || !strings.HasPrefix(r.Status, want.status) || (want.line2 != "" && r.Line2 != want.line2) {
			t.Errorf("row %s: %s/%s %q %q, want %s %q %q", id, r.State, r.Detail, r.Status, r.Line2, want.state, want.status, want.line2)
		}
	}
	if !v.Rows[s.asked].Here || v.Rows[s.limited].Here {
		t.Errorf("here: laptop row %v, gpu-box row %v", v.Rows[s.asked].Here, v.Rows[s.limited].Here)
	}

	// The same from the gpu-box's own board: another machine, the same ids, order and words.
	g := f.waitSame(5*time.Second, func() boardView { return f.gpu.boardView() })
	if strings.Join(g.order(), ",") != strings.Join(v.order(), ",") || strings.Join(g.NeedsYou, ",") != strings.Join(v.NeedsYou, ",") {
		t.Errorf("gpu-box board order %v / %v, laptop %v / %v", g.order(), g.NeedsYou, v.order(), v.NeedsYou)
	}
	if !g.Rows[s.limited].Here || g.Rows[s.asked].Here {
		t.Errorf("gpu-box here flags: own %v, laptop's %v", g.Rows[s.limited].Here, g.Rows[s.asked].Here)
	}

	// The text frame carries the question and the words too.
	text := f.l.must("board", "--once", "--cols", "100")
	for _, w := range []string{"Switchyard", "hub live", "Should I keep the old column?", "perm", "limit"} {
		if !strings.Contains(text, w) {
			t.Errorf("text frame lacks %q:\n%s", w, text)
		}
	}
}

// E2: live. `sy board --follow --ndjson` is running; a Stop whose last message ends "Should
// I keep the old column?" arrives. Within 2 s a line has that row `asked` with the question,
// and counts.needs_you is one more.
func TestE2Live(t *testing.T) {
	f := newFleet(t)
	f.seed()
	fol := f.l.follow()
	first := fol.wait(0, 15*time.Second, "the first live line", func(v boardView) bool { return v.Hub.State == "live" && len(v.Rows) == 6 })
	n0 := first.Counts.NeedsYou
	lines := fol.count()

	const sid = "e2000000-0000-4000-8000-000000000001"
	f.l.hookNoFlush(f.l.slot(2), sid, "UserPromptSubmit", map[string]any{"prompt": "tidy the schema"})
	f.l.hookNoFlush(f.l.slot(2), sid, "Stop", map[string]any{"last_assistant_message": "Dropped the index.\n\nShould I keep the old column?"})
	f.l.flush()
	start := time.Now()
	v := fol.wait(lines, 2*time.Second, "the new row is asked with its question", func(v boardView) bool {
		r, ok := v.Rows[sid]
		return ok && r.State == "asked" && r.Line2 == "Should I keep the old column?" && r.Kind == "question"
	})
	t.Logf("flush to asked line: %v", time.Since(start))
	if v.Counts.NeedsYou != n0+1 {
		t.Errorf("counts.needs_you = %d, want %d", v.Counts.NeedsYou, n0+1)
	}
	if !contains(v.NeedsYou, sid) {
		t.Errorf("needs_you %v lacks %s", v.NeedsYou, sid)
	}
	f.waitSame(5*time.Second, func() boardView { return fol.last() })
}

func contains(xs []string, x string) bool {
	for _, e := range xs {
		if e == x {
			return true
		}
	}
	return false
}

// E3: hub down and back. With the hub stopped, within 5 s the board is offline, the
// gpu-box's rows are greyed and the laptop's rows still follow new hooks; with the hub
// started again it is live and no row is duplicated.
func TestE3HubDownAndBack(t *testing.T) {
	f := newFleet(t)
	s := f.seed()
	fol := f.l.follow()
	fol.wait(0, 15*time.Second, "live with six rows", func(v boardView) bool { return v.Hub.State == "live" && len(v.Rows) == 6 })

	lines := fol.count()
	f.hub.kill()
	off := fol.wait(lines, 5*time.Second, "hub offline", func(v boardView) bool { return v.Hub.State == "offline" })
	if off.Hub.OfflineSince == nil {
		t.Error("offline without offline_since")
	}
	off = fol.wait(lines, 5*time.Second, "other machines greyed", func(v boardView) bool {
		r, ok := v.Rows[s.limited]
		return v.Hub.State == "offline" && ok && r.Greyed
	})
	for id, r := range off.Rows {
		other := id == s.limited || id == s.working || id == s.gdon
		if r.Greyed != other {
			t.Errorf("offline row %s (gpu-box %v): greyed = %v", id, other, r.Greyed)
		}
	}

	// A new hook on the laptop shows while the hub is down; an existing row follows too.
	const fresh = "e3000000-0000-4000-8000-000000000001"
	lines = fol.count()
	f.l.hookNoFlush(f.l.slot(3), fresh, "UserPromptSubmit", map[string]any{"prompt": "go"})
	f.l.hookNoFlush(f.l.slot(1), s.perm, "UserPromptSubmit", map[string]any{"prompt": "yes"})
	f.l.flush() // fails quietly: the hub is down
	live := fol.wait(lines, 8*time.Second, "laptop rows follow hooks while offline", func(v boardView) bool {
		r, ok := v.Rows[fresh]
		p := v.Rows[s.perm]
		return v.Hub.State == "offline" && ok && r.State == "working" && !r.Greyed && p.State == "working"
	})
	if g := live.Rows[s.limited]; !g.Greyed {
		t.Errorf("gpu-box row not greyed after the laptop's hooks: %+v", g)
	}

	// The hub comes back: live again, one row per session.
	lines = fol.count()
	f.hub.start()
	f.l.flush()
	back := fol.wait(lines, 20*time.Second, "hub live again with seven rows", func(v boardView) bool {
		if v.Hub.State != "live" || len(v.Rows) != 7 {
			return false
		}
		for _, r := range v.Rows {
			if r.Greyed {
				return false
			}
		}
		return true
	})
	seen := map[string]int{}
	for _, id := range back.order() {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %s appears %d times", id, n)
		}
	}
	if len(seen) != len(back.Rows) || back.Counts.Sessions != 7 {
		t.Errorf("%d ordered ids, %d rows, counts %+v; want 7", len(seen), len(back.Rows), back.Counts)
	}
	f.waitSame(10*time.Second, func() boardView { return fol.last() })
	f.waitSame(5*time.Second, func() boardView { return f.l.boardView() })
}

// E4: no hub. A fresh home with no hub.url: `sy board --once --json` equals `sy status
// --json` on ids, states and order, and the text frame says there is no hub (nothing to
// connect).
func TestE4NoHub(t *testing.T) {
	l := newLaptop(t)
	seats(t, l.home)
	ids := []string{"e4000000-0000-4000-8000-000000000001", "e4000000-0000-4000-8000-000000000002", "e4000000-0000-4000-8000-000000000003", "e4000000-0000-4000-8000-000000000004"}
	l.hookNoFlush(l.slot(2), ids[0], "Stop", map[string]any{"last_assistant_message": "Which one?\n\nShould I keep the old column?"})
	l.hookNoFlush(l.slot(1), ids[1], "UserPromptSubmit", map[string]any{"prompt": "go"})
	l.hookNoFlush(l.slot(3), ids[2], "Stop", map[string]any{"last_assistant_message": "All done."})
	l.hookNoFlush(l.slot(1), ids[3], "PermissionRequest", map[string]any{"tool_name": "Bash"})

	if got := l.sy("config", "get", "hub.url"); strings.TrimSpace(got.out) != "" {
		t.Fatalf("hub.url is set: %q", got.out)
	}
	v := l.boardView()
	if v.Source != "local" || v.Hub.State != "none" {
		t.Fatalf("source %q, hub %q; want local / none", v.Source, v.Hub.State)
	}
	var st struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			State     string `json:"state"`
			Detail    string `json:"detail"`
			NeedsYou  bool   `json:"needs_you"`
		} `json:"sessions"`
	}
	l.mustJSON(&st, "status", "--json")
	if len(st.Sessions) != len(ids) {
		t.Fatalf("sy status lists %d sessions, want %d", len(st.Sessions), len(ids))
	}
	var want []string
	for _, s := range st.Sessions {
		want = append(want, s.SessionID)
		r, ok := v.Rows[s.SessionID]
		if !ok {
			t.Errorf("board lacks %s", s.SessionID)
			continue
		}
		if r.State != s.State || r.Detail != s.Detail || r.NeedsYou != s.NeedsYou {
			t.Errorf("%s: board %s/%s/%v, status %s/%s/%v", s.SessionID, r.State, r.Detail, r.NeedsYou, s.State, s.Detail, s.NeedsYou)
		}
	}
	if len(v.Rows) != len(st.Sessions) {
		t.Errorf("board has %d rows, status %d", len(v.Rows), len(st.Sessions))
	}
	// Order: the board's groups (seats) in order, rows inside in status order, which is the
	// rank order of sy status: the needs-you list is the same ids in the same order.
	var needs []string
	for _, id := range want {
		if v.Rows[id].NeedsYou {
			needs = append(needs, id)
		}
	}
	if strings.Join(v.NeedsYou, ",") != strings.Join(needs, ",") {
		t.Errorf("needs_you %v, status order %v", v.NeedsYou, needs)
	}
	for _, g := range v.Groups {
		var inGroup []string
		for _, id := range want {
			if v.Rows[id].Machine != "" && containsRow(g.Rows, id) {
				inGroup = append(inGroup, id)
			}
		}
		if strings.Join(inGroup, ",") != strings.Join(g.Rows, ",") {
			t.Errorf("group %q rows %v, status order %v", g.Label, g.Rows, inGroup)
		}
	}

	// The local board says it is local: no hub, nothing to connect.
	text := l.must("board", "--once", "--cols", "100")
	if !strings.Contains(text, "· local") {
		t.Errorf("text frame does not say the source is local:\n%s", text)
	}
	if strings.Contains(text, "sy board connect") {
		t.Errorf("a machine without a hub is told to connect:\n%s", text)
	}
	// --source hub with no hub is exit 4.
	if r := l.sy("board", "--once", "--json", "--source", "hub"); r.code != 4 {
		t.Errorf("--source hub with no hub: exit %d, want 4", r.code)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(l.must("board", "--once", "--json")), &raw); err != nil || raw["v"] != float64(1) {
		t.Errorf("view json: %v", err)
	}
}

func containsRow(rows []string, id string) bool { return contains(rows, id) }
