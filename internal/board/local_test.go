package board

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/record"
	"github.com/mraj602/switchyard/internal/status"
	"github.com/mraj602/switchyard/internal/termenv"
)

func fakeEnv(t testing.TB) termenv.Env { return termenv.Fake("/home/u", "linux", nil) }

func buildFixture(t *testing.T, mod func(*LocalOptions)) View {
	t.Helper()
	lo := fixtureOptions(t)
	if mod != nil {
		mod(&lo)
	}
	v, err := BuildLocal(lo)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Check(); err != nil {
		t.Fatal(err)
	}
	return v
}

// The committed fixture is what fixtureRecords writes.
func TestFixtureUpToDate(t *testing.T) {
	dir := t.TempDir()
	writeOutbox(t, dir, fixtureRecords())
	want, _ := os.ReadFile(filepath.Join(dir, "2026-10-12.jsonl"))
	got, err := os.ReadFile(filepath.Join("testdata", "local", "outbox", "2026-10-12.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("testdata/local is stale: go test ./internal/board -run TestWriteLocalFixture -update")
	}
}

// The local View equals `sy status --json` on ids, states, order and question.
func TestLocalMatchesStatus(t *testing.T) {
	lo := fixtureOptions(t)
	v := buildFixture(t, nil)
	rows, err := status.Collect(status.Options{OutboxDir: lo.OutboxDir, Home: "/home/u", Labels: lo.Labels, Now: fxNow})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(v.Rows) || len(rows) != 10 {
		t.Fatalf("status has %d rows, view %d", len(rows), len(v.Rows))
	}
	var statusNeeds []string
	pos := map[string]int{}
	for i, r := range rows {
		pos[r.SessionID] = i
		got, ok := v.Rows[r.SessionID]
		if !ok {
			t.Fatalf("view lacks %s", r.SessionID)
		}
		if got.State != string(r.State) || got.Detail != r.Detail || got.NeedsYou != r.NeedsYou {
			t.Errorf("%s: view %s/%s/%v, status %s/%s/%v", r.SessionID, got.State, got.Detail, got.NeedsYou, r.State, r.Detail, r.NeedsYou)
		}
		if r.NeedsYou {
			statusNeeds = append(statusNeeds, r.SessionID)
		}
		if r.State == "asked" && r.Question != "" && got.Line2 != r.Question {
			t.Errorf("%s: line2 %q, status question %q", r.SessionID, got.Line2, r.Question)
		}
		if r.Account != "-" && got.Account != r.Account && got.Seat != "" {
			t.Errorf("%s: account %q vs %q", r.SessionID, got.Account, r.Account)
		}
		if r.Project != got.Project {
			t.Errorf("%s: project %q vs %q", r.SessionID, got.Project, r.Project)
		}
	}
	if !reflect.DeepEqual(v.NeedsYou, statusNeeds) {
		t.Errorf("needs_you %v, status %v", v.NeedsYou, statusNeeds)
	}
	for _, g := range v.Groups {
		last := -1
		for _, id := range g.Rows {
			if pos[id] < last {
				t.Errorf("group %s lists %v out of status order", g.Label, g.Rows)
			}
			last = pos[id]
		}
	}
}

func TestLocalRows(t *testing.T) {
	v := buildFixture(t, nil)
	cases := []struct {
		id, status, glyph, line2, kind string
	}{
		{"s-working", "3m", GlyphWorking, "", Line2None},
		{"s-asked", "asked", GlyphNeeds, "Should I update the migration, or keep the old column?", Line2Question},
		{"s-asktool", "asked · AskUserQuestion", GlyphNeeds, "Which database?", Line2Question},
		{"s-dialog", "asked · dialog", GlyphNeeds, "", Line2None},
		{"s-perm", "perm · Bash", GlyphNeeds, "", Line2None},
		{"s-limit", "limit · 17:00", GlyphFailed, "rate limit", Line2Error},
		{"s-done", "done", GlyphDone, "Updated the post. All tests pass.", Line2Recap},
		{"s-nomsg", "done", GlyphDone, "", Line2None},
		{"s-new", "new", GlyphDone, "", Line2None},
		{"s-nobody", "done", GlyphDone, "Done.", Line2Recap},
	}
	for _, c := range cases {
		r, ok := v.Rows[c.id]
		if !ok {
			t.Errorf("%s missing", c.id)
			continue
		}
		if r.Status != c.status || r.Glyph != c.glyph || r.Line2 != c.line2 || r.Line2Kind != c.kind {
			t.Errorf("%s: got status %q glyph %q line2 %q kind %q; want %q %q %q %q", c.id, r.Status, r.Glyph, r.Line2, r.Line2Kind,
				c.status, c.glyph, c.line2, c.kind)
		}
	}
	if _, ok := v.Rows["s-ended"]; ok {
		t.Error("ended session shown without include_ended")
	}
	w := v.Rows["s-working"]
	if w.Title != "migrate-orders" || w.Project != "api" || w.Account != "work" || w.Colour != "#f59e0b" ||
		!w.Here || !w.CanFocus || w.Resume != "claude --resume s-working" {
		t.Errorf("s-working: %+v", w)
	}
	if w.ContextPct == nil || *w.ContextPct != 47 || w.ContextLevel != ContextOK {
		t.Errorf("context %v %s", w.ContextPct, w.ContextLevel)
	}
	if w.ThisSession {
		t.Error("this_session without --session")
	}
	if r := v.Rows["s-limit"]; r.Resume != "claude-2 --resume s-limit" || r.CanFocus {
		t.Errorf("s-limit: %+v", r)
	}
	if v.Source != SourceLocal || v.Hub.State != HubNone || v.Counts.Machines != 1 || v.Counts.Sessions != 10 || v.Counts.NeedsYou != 5 {
		t.Errorf("header: %+v %+v %s", v.Counts, v.Hub, v.Source)
	}
	if want := []string{"s-asked", "s-asktool", "s-dialog", "s-perm", "s-limit"}; len(v.NeedsYou) != 5 {
		t.Errorf("needs_you %v, want the five of %v", v.NeedsYou, want)
	}
}

func TestLocalGroups(t *testing.T) {
	v := buildFixture(t, nil)
	if len(v.Groups) != 3 {
		t.Fatalf("groups %+v", v.Groups)
	}
	work, personal, un := v.Groups[0], v.Groups[1], v.Groups[2]
	if work.Label != "work" || work.Colour != "#f59e0b" || work.WarpColour != "yellow" {
		t.Errorf("work: %+v", work)
	}
	if work.FiveHour == nil || work.FiveHour.Pct != 41 || work.FiveHour.Text != "41% · 15:10" {
		t.Errorf("work 5h: %+v", work.FiveHour)
	}
	if work.SevenDay == nil || work.SevenDay.Text != "30%" || work.Limited {
		t.Errorf("work 7d: %+v limited %v", work.SevenDay, work.Limited)
	}
	if personal.Label != "personal" || !personal.Limited || personal.FiveHour == nil || personal.FiveHour.Text != "100% · 17:00" {
		t.Errorf("personal: %+v", personal)
	}
	if un.Label != UnattributedLabel || len(un.Rows) != 1 || un.Rows[0] != "s-nobody" {
		t.Errorf("unattributed: %+v", un)
	}
	// row colours follow the group
	if v.Rows["s-limit"].Colour != "#8b5cf6" {
		t.Errorf("colour %q", v.Rows["s-limit"].Colour)
	}
}

func TestLocalOptions(t *testing.T) {
	v := buildFixture(t, func(lo *LocalOptions) {
		lo.SessionID = "s-perm"
		lo.IncludeEnded = true
		lo.Machine.ID = "m_other"
	})
	if !v.Rows["s-perm"].ThisSession || v.Rows["s-asked"].ThisSession {
		t.Error("this_session")
	}
	e, ok := v.Rows["s-ended"]
	if !ok || e.Glyph != GlyphEnded || e.Status != "ended" || e.Line2Kind != Line2None {
		t.Errorf("ended: %+v %v", e, ok)
	}
	// the records are from fxMachine, the board runs on m_other: not here, no focus
	// (a recorded focus URL is only usable on the machine it is for... which is the
	// record's machine when term.host is empty, so it still allows it)
	if v.Rows["s-working"].Here {
		t.Error("here for another machine")
	}
	// an empty window
	v2 := buildFixture(t, func(lo *LocalOptions) { lo.OutboxDir = filepath.Join(t.TempDir(), "none") })
	if len(v2.Rows) != 0 || len(v2.Groups) != 2 || v2.Counts.Sessions != 0 {
		t.Errorf("empty outbox: %+v", v2.Groups)
	}
}

// Records older than seven days are ignored.
func TestLocalWindow(t *testing.T) {
	v := buildFixture(t, func(lo *LocalOptions) { lo.Now = fxNow.Add(8 * 24 * time.Hour) })
	if len(v.Rows) != 0 {
		t.Errorf("%d rows from an 8 day old outbox", len(v.Rows))
	}
}

// A focus URL that fails the allow-list never gives can_focus, and a host elsewhere does
// not either.
func TestLocalFocusRules(t *testing.T) {
	recs := fixtureRecords()
	for _, r := range recs {
		if r.Term != nil {
			r.Term.FocusURL = "https://evil.example/x"
		}
	}
	dir := t.TempDir()
	writeOutbox(t, dir, recs)
	lo := fixtureOptions(t)
	lo.OutboxDir = dir
	v, err := BuildLocal(lo)
	if err != nil {
		t.Fatal(err)
	}
	if v.Rows["s-working"].CanFocus {
		t.Error("can_focus for https URL")
	}
	for _, r := range recs {
		if r.Term != nil {
			r.Term.FocusURL, r.Term.Host = fxFocus, "m_laptop"
		}
	}
	writeOutbox(t, dir, recs)
	lo.Machine.ID = fxMachine
	v, _ = BuildLocal(lo)
	if v.Rows["s-working"].CanFocus {
		t.Error("can_focus for a terminal on another machine")
	}
}

func TestLocalSourceStreams(t *testing.T) {
	dir := t.TempDir()
	recs := fixtureRecords()
	writeOutbox(t, dir, recs[:3])
	lo := fixtureOptions(t)
	lo.OutboxDir = dir
	lo.Now = time.Time{} // real clock: records are old, but the window check is in the test below
	lo.PollEvery, lo.Debounce, lo.Heartbeat = 10*time.Millisecond, 20*time.Millisecond, time.Hour
	lo.Options.Now = fxNow
	src := NewLocalSource(lo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := src.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := recv(t, ch)
	if len(first.Rows) != 2 {
		t.Fatalf("first view has %d rows, want 2", len(first.Rows))
	}
	if _, err := src.Start(ctx); err == nil {
		t.Error("second Start accepted")
	}
	// the outbox grows: a new view follows
	writeOutbox(t, dir, recs[:8])
	next := recv(t, ch)
	if len(next.Rows) <= 2 {
		t.Fatalf("view after growth has %d rows", len(next.Rows))
	}
	// no change: nothing arrives
	select {
	case v := <-ch:
		t.Fatalf("unexpected view with %d rows", len(v.Rows))
	case <-time.After(150 * time.Millisecond):
	}
	// Refresh refolds, but an identical view is not sent again
	src.Refresh()
	select {
	case <-ch:
		t.Fatal("identical view sent")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	for range ch { // closed after ctx ends
	}
}

func recv(t *testing.T, ch <-chan View) View {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no view")
	}
	return View{}
}

// The outbox is only read.
func TestLocalReadsOnly(t *testing.T) {
	dir := t.TempDir()
	writeOutbox(t, dir, fixtureRecords())
	before, _ := os.ReadDir(dir)
	lo := fixtureOptions(t)
	lo.OutboxDir = dir
	if _, err := BuildLocal(lo); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Error("files appeared")
	}
	_ = record.Version
}
