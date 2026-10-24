package board

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

// The local fixture: one outbox day file under testdata/local/outbox. `go test
// ./internal/board -run TestWriteLocalFixture -update` rewrites it from fixtureRecords.

const (
	fxMachine = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"
	fxHome    = "/home/u"
	fxFocus   = "warp://session/0123456789abcdef0123456789abcdef"
)

// fxNow is "now" for every test over the fixture.
var fxNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

var (
	fxWork     = &record.Seat{Account: "3f1c6a2e-0000-4000-8000-000000000001", Org: "o1", Email: "me@work.example"}
	fxPersonal = &record.Seat{Account: "3f1c6a2e-0000-4000-8000-000000000002", Org: "o2", Email: "me@home.example"}
)

type fxBuilder struct {
	n    int
	recs []*record.Record
}

func (b *fxBuilder) add(kind string, at string, slot string, seat *record.Seat, term *record.Term, payload map[string]any, derived map[string]any) {
	ts, err := time.Parse(time.RFC3339, at)
	if err != nil {
		panic(err)
	}
	b.n++
	r := &record.Record{
		V: 1, ID: fmt.Sprintf("01JA2XRB3T0M2N4P6Q8R%04d", b.n), Kind: kind, TS: record.FormatTime(ts),
		Machine: fxMachine, Slot: slot, Seat: seat, Term: term, Client: "wy/0.7.0", Derived: record.Derived{},
	}
	for k, v := range derived {
		r.Derived.Set(k, v)
	}
	if err := r.SetPayload(payload); err != nil {
		panic(err)
	}
	b.recs = append(b.recs, r)
}

func (b *fxBuilder) hook(at, slot string, seat *record.Seat, sid, name, cwd string, extra map[string]any, derived map[string]any) {
	p := map[string]any{"hook_event_name": name, "session_id": sid, "cwd": cwd}
	for k, v := range extra {
		p[k] = v
	}
	var term *record.Term
	if sid == "s-working" {
		term = &record.Term{App: "warp", ID: "x", FocusURL: fxFocus}
	}
	b.add(record.KindHookEvent, at, slot, seat, term, p, derived)
}

// fixtureRecords is the scenario of testdata/local: two accounts and eleven sessions.
func fixtureRecords() []*record.Record {
	b := &fxBuilder{}
	w, p := "~/.claude", "~/.claude-2"
	// working, with a terminal and a title
	b.hook("2026-10-12T11:57:00Z", w, fxWork, "s-working", "UserPromptSubmit", "/home/u/api", nil, nil)
	b.add(record.KindLimitSample, "2026-10-12T11:58:00Z", w, fxWork, nil, map[string]any{
		"session_id": "s-working", "session_name": "migrate-orders"},
		map[string]any{"five_hour_pct": 41, "five_hour_resets_at": "2026-10-12T15:10:00Z", "seven_day_pct": 30,
			"seven_day_resets_at": "2026-10-15T18:00:00Z", "context_pct": 47.4})
	// asked: a plain text question at the end of a turn
	b.hook("2026-10-12T11:50:00Z", w, fxWork, "s-asked", "Stop", "/home/u/web", map[string]any{"stop_hook_active": false},
		map[string]any{"last_message": "I changed the column.\n\nShould I update the migration, or keep the old column?"})
	// asked through a tool
	b.hook("2026-10-12T11:52:00Z", w, fxWork, "s-asktool", "PreToolUse", "/home/u/web", map[string]any{"tool_name": "AskUserQuestion"},
		map[string]any{"question": "Which database?"})
	// asked through a dialog
	b.hook("2026-10-12T11:53:00Z", p, fxPersonal, "s-dialog", "Notification", "/home/u/notes", map[string]any{"notification_type": "elicitation_dialog"}, nil)
	// needs permission, naming the tool
	b.hook("2026-10-12T11:55:00Z", w, fxWork, "s-perm", "PermissionRequest", "/home/u/infra", map[string]any{"tool_name": "Bash"}, nil)
	// failed: rate limit, with the personal seat's 5-hour reset
	b.hook("2026-10-12T11:40:00Z", p, fxPersonal, "s-limit", "StopFailure", "/home/u/blog", map[string]any{"error": "rate_limit"}, nil)
	b.add(record.KindLimitSample, "2026-10-12T11:41:00Z", p, fxPersonal, nil, map[string]any{"session_id": "s-limit"},
		map[string]any{"five_hour_pct": 100, "five_hour_resets_at": "2026-10-12T17:00:00Z", "seven_day_pct": 62})
	// done with a recap that holds a code fence
	b.hook("2026-10-12T11:30:00Z", p, fxPersonal, "s-done", "Stop", "/home/u/blog", map[string]any{"stop_hook_active": false},
		map[string]any{"last_message": "Updated the post.\n```go\nfmt.Println(1)\n```\nAll tests pass."})
	// done with no last message (privacy)
	b.hook("2026-10-12T11:20:00Z", p, fxPersonal, "s-nomsg", "Stop", "/home/u/blog2", map[string]any{"stop_hook_active": false}, nil)
	// new
	b.hook("2026-10-12T11:59:00Z", w, fxWork, "s-new", "SessionStart", "/home/u/fresh", map[string]any{"source": "startup"}, nil)
	// ended (hidden unless include_ended)
	b.hook("2026-10-12T10:00:00Z", w, fxWork, "s-ended", "SessionEnd", "/home/u/old", map[string]any{"reason": "clear"}, nil)
	// no seat
	b.hook("2026-10-12T11:45:00Z", "", nil, "s-nobody", "Stop", "/tmp/scratch", map[string]any{"stop_hook_active": false}, map[string]any{"last_message": "Done."})
	return b.recs
}

func writeOutbox(t testing.TB, dir string, recs []*record.Record) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(append(out, line...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-10-12.jsonl"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteLocalFixture(t *testing.T) {
	if !*update {
		t.Skip("run with -update to rewrite testdata/local")
	}
	writeOutbox(t, filepath.Join("testdata", "local", "outbox"), fixtureRecords())
}

// fixtureOptions are the options every fixture test uses.
func fixtureOptions(t testing.TB) LocalOptions {
	t.Helper()
	return LocalOptions{
		Options:   Options{Now: fxNow, Loc: time.UTC, Machine: Machine{ID: fxMachine, Name: "laptop"}},
		Env:       fakeEnv(t),
		OutboxDir: filepath.Join("testdata", "local", "outbox"),
		Labels:    map[string]string{"~/.claude": "work", "~/.claude-2": "personal"},
		Colours:   map[string]string{"~/.claude": "#f59e0b", "~/.claude-2": "#8b5cf6"},
	}
}
