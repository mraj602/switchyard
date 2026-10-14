package tui

import (
	"strconv"
	"time"

	"github.com/mraj602/switchyard/internal/board"
)

const (
	idAsked = "c41e2a90-5b7d-4c1e-9f0a-2d3b4c5e6f70"
	idPerm  = "9a0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"
	idWork  = "11111111-2222-4333-8444-555555555555"
	idLimit = "5f6e7d8c-9b0a-4b1c-8d2e-3f4a5b6c7d8e"
	idDone  = "77777777-2222-4333-8444-555555555555"
	idRem   = "88888888-2222-4333-8444-555555555555"
	idOld   = "99999999-2222-4333-8444-555555555555"
)

func ip(n int) *int { return &n }

// fixtureView: three seats, a plain-text question, a permission, a limit, a remote-machine
// row, and context at 79, 80 and 92.
func fixtureView() board.View {
	t0 := time.Date(2026, 10, 20, 14, 6, 0, 0, time.UTC)
	rows := map[string]board.Row{}
	add := func(r board.Row) { rows[r.ID] = r }
	base := func(id, seat, acct, colour string) board.Row {
		return board.Row{ID: id, Seat: seat, Account: acct, Colour: colour, Machine: "m_laptop", MachineName: "laptop", Here: true, Since: t0, ContextLevel: board.ContextOK, Line2Kind: board.Line2None}
	}
	r := base(idAsked, "s_work", "work", "#f59e0b")
	r.State, r.Detail, r.NeedsYou, r.Glyph, r.Status, r.Project, r.Title = "asked", "question", true, board.GlyphNeeds, "asked", "api", "migrate-orders"
	r.ContextPct, r.Age, r.Line2, r.Line2Kind, r.CanFocus = ip(79), "4m", "Update the migration, or keep the old column?", board.Line2Question, true
	r.Resume = "claude-2 --resume " + idAsked
	add(r)
	r = base(idPerm, "s_work", "work", "#f59e0b")
	r.State, r.Detail, r.NeedsYou, r.Glyph, r.Status, r.Project, r.Title = "needs_permission", "permission", true, board.GlyphNeeds, "perm · Bash", "storefront", "deploy"
	r.ContextPct, r.ContextLevel, r.Age = ip(80), board.ContextWarn, "1m"
	add(r)
	r = base(idWork, "s_work", "work", "#f59e0b")
	r.State, r.Glyph, r.Status, r.Project, r.Title = "working", board.GlyphWorking, "2m", "api", "flaky-tests"
	r.ContextPct, r.ContextLevel, r.Age, r.Line2, r.Line2Kind = ip(92), board.ContextHigh, "2m", "Running the suite again with -race.", board.Line2Recap
	add(r)
	r = base(idLimit, "s_personal", "personal", "#3b82f6")
	r.State, r.Detail, r.Glyph, r.Status, r.Project, r.Title = "failed", "rate_limit", board.GlyphFailed, "limit · 17:00", "notes", "weekly-review"
	r.Age, r.Line2, r.Line2Kind = "12m", "Usage limit reached", board.Line2Error
	add(r)
	r = base(idDone, "s_personal", "personal", "#3b82f6")
	r.State, r.Glyph, r.Status, r.Project, r.Title, r.Age = "done", board.GlyphDone, "done", "blog", "draft-post", "20m"
	r.Line2, r.Line2Kind = "Draft saved.", board.Line2Recap
	add(r)
	r = base(idRem, "s_client", "client", "#10b981")
	r.Machine, r.MachineName, r.Here = "m_gpu", "gpu-box", false
	r.State, r.Glyph, r.Status, r.Project, r.Title, r.Age = "working", board.GlyphWorking, "40s", "infra", "tf-plan", "40s"
	r.RemoteURL = "https://claude.ai/code/session_abc"
	r.Resume = "claude-3 --resume " + idRem
	add(r)
	r = base(idOld, "s_client", "client", "#10b981")
	r.State, r.Glyph, r.Status, r.Project, r.Title, r.Age = "ended", board.GlyphEnded, "ended", "old", "chore", "1d"
	add(r)

	return board.View{
		V: board.Version, GeneratedAt: t0, Source: board.SourceHub,
		Hub:      board.HubInfo{State: board.HubLive, URL: "https://hub.example"},
		Machine:  board.Machine{ID: "m_laptop", Name: "laptop"},
		Counts:   board.Counts{Sessions: 6, Machines: 2, NeedsYou: 2},
		NeedsYou: []string{idAsked, idPerm},
		Groups: []board.Group{
			{Seat: "s_work", Label: "work", Colour: "#f59e0b", WarpColour: "yellow",
				FiveHour: &board.Window{Pct: 41, Text: "41% · 15:10"}, SevenDay: &board.Window{Pct: 30, Text: "30%"},
				Rows: []string{idAsked, idPerm, idWork}},
			{Seat: "s_personal", Label: "personal", Colour: "#3b82f6", WarpColour: "blue", Limited: true,
				FiveHour: &board.Window{Pct: 100, Text: "100% · 17:00"}, SevenDay: &board.Window{Pct: 64, Text: "64%"},
				Rows: []string{idLimit, idDone}},
			{Seat: "s_client", Label: "client", Colour: "#10b981", WarpColour: "green",
				Rows: []string{idRem, idOld}},
		},
		Rows: rows,
	}
}

// bigView has n working rows in one account, for the redraw budget and scrolling.
func bigView(n int) board.View {
	v := board.View{V: board.Version, Source: board.SourceLocal, Hub: board.HubInfo{State: board.HubNone},
		Machine: board.Machine{ID: "m", Name: "laptop"}, Counts: board.Counts{Sessions: n, Machines: 1},
		Rows: map[string]board.Row{}}
	g := board.Group{Seat: "s", Label: "work", Colour: "#f59e0b"}
	for i := 0; i < n; i++ {
		id := "id-" + strconv.Itoa(i)
		v.Rows[id] = board.Row{ID: id, Glyph: board.GlyphWorking, Status: "2m", Project: "proj" + strconv.Itoa(i), Title: "task number " + strconv.Itoa(i),
			Account: "work", Colour: "#f59e0b", Here: true, ContextPct: ip(i % 100), ContextLevel: board.ContextOK, Age: "2m",
			Line2: "doing a thing that takes a while to finish", Line2Kind: board.Line2Recap}
		g.Rows = append(g.Rows, id)
	}
	v.Groups = []board.Group{g}
	return v
}
