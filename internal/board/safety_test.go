package board

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/record"
)

// nasty pieces: escape sequences, bidi controls, C0 and C1 controls, line breaks, invisible
// characters, broken UTF-8, wide text.
var nasty = []string{
	"\x1b]2;x\x07", "\x1b]0;title\x1b\\", "\x1b[31m", "\x1b[2J", "\x1b", "\x07", "\r", "\n", "\r\n", "\t", "\x00", "\x7f",
	"\u202e", "\u202d", "\u202a", "\u202c", "\u2066", "\u2067", "\u2069", "\u200e", "\u200f", "\u061c",
	"\u009d", "\u009b", "\u0085", "\u0090", "\u009c", "\u2028", "\u2029", "\u200b", "\ufeff", "\xff", "\xc3",
	"日本語", "😀", "é", "api", "migrate", "?", "Should I do it?", " ", "```", "``` code ```", "'", "\"", "<", "`",
}

func junk(r *rand.Rand, maxParts int) string {
	var b strings.Builder
	for i, n := 0, 1+r.Intn(maxParts); i < n; i++ {
		b.WriteString(nasty[r.Intn(len(nasty))])
	}
	return b.String()
}

// assertClean fails when any string of v (values and map keys) holds a C0 or C1 control,
// ESC, DEL or a bidi override.
func assertClean(t testing.TB, v View) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(path string, x any)
	check := func(path, s string) {
		for _, r := range s {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) ||
				(r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c || r == 0x2028 || r == 0x2029 {
				t.Fatalf("%s holds %U: %q", path, r, s)
			}
		}
	}
	walk = func(path string, x any) {
		switch x := x.(type) {
		case string:
			check(path, x)
		case []any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		case map[string]any:
			for k, e := range x {
				check(path+" key", k)
				walk(path+"."+k, e)
			}
		}
	}
	walk("view", doc)
}

// 10k random hostile values through the hub builder, every string field of a session and a
// seat.
func TestHubTextSafetyFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	states := []string{"working", "needs_permission", "asked", "done", "failed", "ended", "gone"}
	details := []string{"", "question", "elicitation", "tool", "rate_limit", "billing_error", "started", "permission_request"}
	o := hubOptions()
	o.IncludeEnded = true
	for i := 0; i < 10000; i++ {
		s := HubSession{
			ID: junk(r, 3), State: states[r.Intn(len(states))], Detail: sp(details[r.Intn(len(details))]),
			StateSince: tp(hubNow.Add(-time.Duration(r.Intn(3000)) * time.Second)),
			Question:   sp(junk(r, 6)), LastMessage: sp(junk(r, 10)), Seat: sp(junk(r, 2)), SeatLabel: sp(junk(r, 3)),
			SeatColour: sp(junk(r, 2)), Machine: sp(junk(r, 2)), MachineName: sp(junk(r, 3)), Project: sp(junk(r, 4)),
			Cwd: sp(junk(r, 4)), Name: sp(junk(r, 4)), Title: sp(junk(r, 4)), Resume: sp(junk(r, 4)),
			RemoteURL: sp("https://claude.ai/" + junk(r, 3)), FocusURL: sp(junk(r, 2)), Tool: sp(junk(r, 3)),
		}
		seat := HubSeat{ID: junk(r, 2), Label: sp(junk(r, 3)), Email: sp(junk(r, 3)), Colour: sp(junk(r, 2)),
			Windows: []HubWindow{{Key: "five_hour", UsedPct: fp(r.Float64() * 120), ResetsAt: tp(hubNow.Add(time.Hour))}}}
		o.Machine = Machine{ID: junk(r, 2), Name: junk(r, 3)}
		o.Hub.URL = junk(r, 2)
		if i%3 == 0 {
			s.Seat = &seat.ID
		}
		v := BuildHub(HubData{Seats: []HubSeat{seat}, Sessions: []HubSession{s}}, o)
		assertClean(t, v)
		for _, row := range v.Rows {
			if row.RemoteURL != "" && (!strings.HasPrefix(row.RemoteURL, "https://claude.ai/") || strings.ContainsAny(row.RemoteURL, " \"'<>\x1b")) {
				t.Fatalf("remote_url %q", row.RemoteURL)
			}
			if row.CanFocus {
				t.Fatalf("can_focus for %q", *s.FocusURL)
			}
		}
	}
}

// Hostile records through the local source: cwd, question, last message, labels, email,
// machine names.
func TestLocalTextSafetyFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for i := 0; i < 150; i++ {
		b := &fxBuilder{}
		slot := "~/.claude-2"
		seat := &record.Seat{Account: "acct-" + junk(r, 2) + "-xxxxxxxx", Org: "o", Email: junk(r, 3)}
		b.hook("2026-10-12T11:50:00Z", slot, seat, "s1", "Stop", "/home/u/"+junk(r, 4), map[string]any{"stop_hook_active": false},
			map[string]any{"last_message": junk(r, 8) + "\n\n" + junk(r, 3) + "?"})
		b.hook("2026-10-12T11:51:00Z", slot, seat, "s2", "PreToolUse", junk(r, 4), map[string]any{"tool_name": "AskUserQuestion"},
			map[string]any{"question": junk(r, 6)})
		b.hook("2026-10-12T11:52:00Z", slot, seat, "s3", "PermissionRequest", "/x", map[string]any{"tool_name": junk(r, 3)}, nil)
		b.add(record.KindLimitSample, "2026-10-12T11:53:00Z", slot, seat, nil, map[string]any{"session_id": "s1", "session_name": junk(r, 5)}, nil)
		dir := t.TempDir()
		writeOutbox(t, dir, b.recs)
		lo := fixtureOptions(t)
		lo.OutboxDir = dir
		lo.Labels = map[string]string{slot: junk(r, 3)}
		lo.Machine = Machine{ID: fxMachine, Name: junk(r, 3)}
		v, err := BuildLocal(lo)
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Rows) != 3 {
			t.Fatalf("%d rows", len(v.Rows))
		}
		assertClean(t, v)
	}
}

// The example of the spec: a title-setting sequence, a bidi override and a carriage return in
// a project name.
func TestProjectNameInjection(t *testing.T) {
	for _, name := range []string{"\x1b]2;x\x07", "\u202e", "\r", "api\x1b]2;pwn\x07\u202e\r"} {
		s := hubSession("a", "done", "")
		s.Project = sp(name)
		v := BuildHub(HubData{Sessions: []HubSession{s}}, hubOptions())
		assertClean(t, v)
		if got := v.Rows["a"].Project; strings.ContainsAny(got, "\x1b\x07\r\u202e") {
			t.Errorf("project %q", got)
		}
	}
}

func FuzzClean(f *testing.F) {
	for _, s := range nasty {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Line(s)
		for _, r := range out {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
				t.Fatalf("Line(%q) = %q holds %U", s, out, r)
			}
		}
		if c := Cut(out, 10); Width(c) > 10 {
			t.Fatalf("Cut(%q) is %d columns", out, Width(c))
		}
	})
}

var _ = filepath.Join
