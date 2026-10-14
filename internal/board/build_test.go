package board

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func sp(s string) *string       { return &s }
func fp(f float64) *float64     { return &f }
func tp(t time.Time) *time.Time { return &t }

var hubNow = time.Date(2026, 10, 20, 14, 6, 0, 0, time.UTC)

func hubOptions() Options {
	return Options{Now: hubNow, Loc: time.UTC, Machine: Machine{ID: "m_laptop", Name: "laptop"},
		Hub: HubInfo{State: HubLive, URL: "https://hub.example"}}
}

func hubSeats() []HubSeat {
	return []HubSeat{
		{ID: "s_work", Label: sp("work"), Colour: sp("#f59e0b"), Windows: []HubWindow{
			{Key: "five_hour", UsedPct: fp(41), ResetsAt: tp(hubNow.Add(65 * time.Minute))},
			{Key: "seven_day", UsedPct: fp(30), ResetsAt: tp(hubNow.Add(72 * time.Hour))}}},
		{ID: "s_home", Label: sp("home"), Colour: sp("#8b5cf6"), Windows: []HubWindow{
			{Key: "five_hour", UsedPct: fp(100), ResetsAt: tp(time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC))}}},
		{ID: "s_hidden", Label: sp("old"), Hidden: true},
	}
}

func hubSession(id, st, detail string) HubSession {
	s := HubSession{ID: id, State: st, StateSince: tp(hubNow.Add(-4 * time.Minute)), Seat: sp("s_work"),
		Machine: sp("m_laptop"), MachineName: sp("laptop"), Project: sp("api"), NeedsYou: false}
	if detail != "" {
		s.Detail = sp(detail)
	}
	switch st {
	case "needs_permission", "asked":
		s.NeedsYou = true
	case "failed":
		s.NeedsYou = detail != "overloaded" && detail != "server_error"
	}
	return s
}

// Every state x detail of states.md 1.
func TestHubStateWords(t *testing.T) {
	cases := []struct {
		st, detail string
		seat       string
		status     string
		glyph      string
	}{
		{"working", "", "s_work", "4m", GlyphWorking},
		{"working", "repaired", "s_work", "4m", GlyphWorking},
		{"needs_permission", "permission_request", "s_work", "perm", GlyphNeeds},
		{"needs_permission", "permission_prompt", "s_work", "perm", GlyphNeeds},
		{"asked", "question", "s_work", "asked", GlyphNeeds},
		{"asked", "elicitation", "s_work", "asked · dialog", GlyphNeeds},
		{"asked", "tool", "s_work", "asked", GlyphNeeds},
		{"done", "", "s_work", "done", GlyphDone},
		{"done", "started", "s_work", "new", GlyphDone},
		{"done", "resumed", "s_work", "done", GlyphDone},
		{"done", "repaired", "s_work", "done", GlyphDone},
		{"done", "compact", "s_work", "done", GlyphDone},
		{"failed", "rate_limit", "s_work", "limit · 15:11", GlyphFailed},
		{"failed", "rate_limit", "s_home", "limit · 17:00", GlyphFailed},
		{"failed", "rate_limit", "", "limit", GlyphFailed},
		{"failed", "billing_error", "s_work", "billing error", GlyphFailed},
		{"failed", "authentication_failed", "s_work", "authentication failed", GlyphFailed},
		{"failed", "overloaded", "s_work", "overloaded", GlyphFailed},
		{"failed", "server_error", "s_work", "server error", GlyphFailed},
		{"failed", "", "s_work", "failed", GlyphFailed},
		{"ended", "clear", "s_work", "ended", GlyphEnded},
		{"gone", "gone", "s_work", "gone", GlyphGone},
	}
	o := hubOptions()
	o.IncludeEnded = true
	for _, c := range cases {
		name := c.st + ":" + c.detail + "@" + c.seat
		s := hubSession("x", c.st, c.detail)
		s.Seat = nil
		if c.seat != "" {
			s.Seat = sp(c.seat)
		}
		v := BuildHub(HubData{Seats: hubSeats(), Sessions: []HubSession{s}}, o)
		if err := v.Check(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r, ok := v.Rows["x"]
		if !ok {
			t.Fatalf("%s: row missing", name)
		}
		if r.Status != c.status || r.Glyph != c.glyph || r.State != c.st || r.Detail != c.detail {
			t.Errorf("%s: got status %q glyph %q state %q detail %q; want %q %q", name, r.Status, r.Glyph, r.State, r.Detail, c.status, c.glyph)
		}
		if r.NeedsYou != s.NeedsYou || len(v.NeedsYou) != map[bool]int{true: 1}[s.NeedsYou] {
			t.Errorf("%s: needs_you %v / %v", name, r.NeedsYou, v.NeedsYou)
		}
	}
}

func TestHubLine2(t *testing.T) {
	q := hubSession("q", "asked", "question")
	q.Question = sp("Update the migration, or keep the old column?")
	q.LastMessage = sp("Some long text.\n\nUpdate the migration, or keep the old column?")
	d := hubSession("d", "done", "")
	d.LastMessage = sp("Fixed it.\n```sh\nrm -rf /\n```\nTests pass.   ")
	n := hubSession("n", "done", "")
	n.LastMessage = nil
	w := hubSession("w", "working", "")
	w.LastMessage = sp("Looking at the schema")
	f := hubSession("f", "failed", "billing_error")
	f.LastMessage = sp("ignored")
	e := hubSession("e", "done", "")
	e.LastMessage = sp("  \n ")
	v := BuildHub(HubData{Seats: hubSeats(), Sessions: []HubSession{q, d, n, w, f, e}}, hubOptions())
	want := map[string][2]string{
		"q": {"Update the migration, or keep the old column?", Line2Question},
		"d": {"Fixed it. Tests pass.", Line2Recap},
		"n": {"", Line2None},
		"w": {"Looking at the schema", Line2Recap},
		"f": {"billing error", Line2Error},
		"e": {"", Line2None},
	}
	for id, w := range want {
		r := v.Rows[id]
		if r.Line2 != w[0] || r.Line2Kind != w[1] {
			t.Errorf("%s: line2 %q kind %q, want %q %q", id, r.Line2, r.Line2Kind, w[0], w[1])
		}
	}
}

func TestHubViewShape(t *testing.T) {
	a := hubSession("a", "asked", "question")
	a.StateSince = tp(hubNow.Add(-30 * time.Minute))
	b := hubSession("b", "needs_permission", "permission_request")
	b.StateSince = tp(hubNow.Add(-50 * time.Minute))
	b.Machine, b.MachineName = sp("m_gpu"), sp("gpu-box")
	b.Seat = sp("s_home")
	b.FocusURL = sp("warp://session/0123456789abcdef0123456789abcdef")
	b.RemoteURL = sp("https://claude.ai/code/abc")
	b.ContextPct = fp(92.4)
	c := hubSession("c", "working", "")
	c.Seat = nil
	c.Cwd = sp("/home/u/proj")
	c.Project = nil
	c.RemoteURL = sp("javascript:alert(1)")
	c.Title, c.Name = nil, sp("my-name")
	c.FocusURL = sp("file:///etc/passwd")
	c.Machine = sp("m_laptop")
	d := hubSession("d", "ended", "clear")
	// the overview lists b before a (oldest first); /v1/sessions lists a first
	data := HubData{
		Overview: HubOverview{Machines: []HubMachine{{ID: "m_laptop"}, {ID: "m_gpu"}},
			NeedsYou: []struct {
				ID string `json:"id"`
			}{{"b"}, {"a"}}},
		Seats:    hubSeats(),
		Sessions: []HubSession{a, b, c, d},
	}
	v := BuildHub(data, hubOptions())
	if err := v.Check(); err != nil {
		t.Fatal(err)
	}
	if v.Source != SourceHub || v.Hub.State != HubLive || v.Hub.URL != "https://hub.example" || v.Machine.ID != "m_laptop" {
		t.Errorf("header %+v", v)
	}
	if fmt.Sprint(v.NeedsYou) != "[b a]" {
		t.Errorf("needs_you %v", v.NeedsYou)
	}
	if v.Counts.Sessions != 3 || v.Counts.Machines != 2 || v.Counts.NeedsYou != 2 {
		t.Errorf("counts %+v", v.Counts)
	}
	// groups follow the accounts, the hidden seat is left out, Unattributed is last
	var labels []string
	for _, g := range v.Groups {
		labels = append(labels, g.Label)
	}
	if fmt.Sprint(labels) != "[work home Unattributed]" {
		t.Fatalf("groups %v", labels)
	}
	if fmt.Sprint(v.Groups[0].Rows) != "[a]" || fmt.Sprint(v.Groups[1].Rows) != "[b]" || fmt.Sprint(v.Groups[2].Rows) != "[c]" {
		t.Errorf("group rows %v %v %v", v.Groups[0].Rows, v.Groups[1].Rows, v.Groups[2].Rows)
	}
	if g := v.Groups[0]; g.WarpColour != "yellow" || g.FiveHour.Text != "41% · 15:11" || g.SevenDay.Text != "30%" || g.Limited {
		t.Errorf("work group %+v %+v", g, g.FiveHour)
	}
	if g := v.Groups[1]; !g.Limited || g.FiveHour.Text != "100% · 17:00" {
		t.Errorf("home group %+v", g)
	}
	rb := v.Rows["b"]
	if rb.Here || rb.MachineName != "gpu-box" || !rb.CanFocus || rb.RemoteURL != "https://claude.ai/code/abc" ||
		rb.ContextLevel != ContextHigh || *rb.ContextPct != 92 || rb.Colour != "#8b5cf6" || rb.Age != "50m" {
		t.Errorf("b: %+v", rb)
	}
	rc := v.Rows["c"]
	if !rc.Here || rc.CanFocus || rc.RemoteURL != "" || rc.Project != "proj" || rc.Title != "my-name" || rc.ContextLevel != ContextOK {
		t.Errorf("c: %+v", rc)
	}
	if rc.Colour != neutralColour {
		t.Errorf("unattributed colour %q", rc.Colour)
	}
	if _, ok := v.Rows["d"]; ok {
		t.Error("ended row without include_ended")
	}
	// the focus URL itself is not in the JSON
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "warp://") {
		t.Error("focus URL leaked into the View")
	}
}

func TestEndedWindow(t *testing.T) {
	o := hubOptions()
	o.IncludeEnded = true
	fresh := hubSession("fresh", "ended", "clear")
	old := hubSession("old", "gone", "gone")
	old.StateSince = tp(hubNow.Add(-72 * time.Hour))
	v := BuildHub(HubData{Seats: hubSeats(), Sessions: []HubSession{fresh, old}}, o)
	if _, ok := v.Rows["fresh"]; !ok {
		t.Error("recent ended row missing")
	}
	if _, ok := v.Rows["old"]; ok {
		t.Error("3 day old ended row shown")
	}
}

func TestPermissionTool(t *testing.T) {
	s := hubSession("p", "needs_permission", "permission_request")
	s.Tool = sp("Bash")
	v := BuildHub(HubData{Sessions: []HubSession{s}}, hubOptions())
	if v.Rows["p"].Status != "perm · Bash" {
		t.Errorf("status %q", v.Rows["p"].Status)
	}
}

func TestHubThisSessionAndNoMachine(t *testing.T) {
	o := hubOptions()
	o.SessionID = "a"
	o.Machine = Machine{}
	v := BuildHub(HubData{Sessions: []HubSession{hubSession("a", "done", ""), hubSession("b", "done", "")}}, o)
	if !v.Rows["a"].ThisSession || v.Rows["b"].ThisSession || v.Rows["a"].Here {
		t.Errorf("%+v", v.Rows["a"])
	}
}

// A window whose reset has passed shows its number without a time; a missing percentage
// gives no window.
func TestWindowRollover(t *testing.T) {
	seats := []HubSeat{{ID: "s", Label: sp("x"), Windows: []HubWindow{
		{Key: "five_hour", UsedPct: fp(55), ResetsAt: tp(hubNow.Add(-time.Hour))},
		{Key: "seven_day", UsedPct: nil}}}}
	v := BuildHub(HubData{Seats: seats}, hubOptions())
	g := v.Groups[0]
	if g.FiveHour == nil || g.FiveHour.Text != "55%" || g.FiveHour.ResetsAt != nil || g.SevenDay != nil {
		t.Errorf("%+v %+v", g.FiveHour, g.SevenDay)
	}
	if g.Colour != "#14b8a6" {
		t.Errorf("default colour %q", g.Colour)
	}
}

func TestWarpName(t *testing.T) {
	for hex, want := range map[string]string{
		"#f59e0b": "yellow", "#f43f5e": "red", "#84cc16": "green", "#0ea5e9": "cyan",
		"#ef4444": "red", "#3b82f6": "blue", "#06b6d4": "cyan", "#a855f7": "magenta", "#d946ef": "magenta", "nonsense": "blue",
	} {
		if got := WarpName(hex); got != want {
			t.Errorf("WarpName(%s) = %s, want %s", hex, got, want)
		}
	}
	o := hubOptions()
	o.WarpColour = func(string) string { return "cyan" }
	v := BuildHub(HubData{Seats: hubSeats()}, o)
	if v.Groups[0].WarpColour != "cyan" {
		t.Error("injected WarpColour not used")
	}
}
