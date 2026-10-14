package board

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSortRows(t *testing.T) {
	at := func(m int) time.Time { return hubNow.Add(-time.Duration(m) * time.Minute) }
	rows := []Row{
		{ID: "done-old", State: "done", Since: at(60)},
		{ID: "ended", State: "ended", Since: at(5)},
		{ID: "work-new", State: "working", Since: at(1)},
		{ID: "ask-new", State: "asked", NeedsYou: true, Since: at(2)},
		{ID: "overloaded", State: "failed", Since: at(3)},
		{ID: "work-old", State: "working", Since: at(30)},
		{ID: "ask-old", State: "asked", NeedsYou: true, Since: at(20)},
		{ID: "done-new", State: "done", Since: at(10)},
		{ID: "gone", State: "gone", Since: at(4)},
		{ID: "b-tie", State: "done", Since: at(10)},
	}
	sortRows(rows)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	want := "ask-old ask-new work-new work-old b-tie done-new done-old overloaded ended gone"
	if got := strings.Join(ids, " "); got != want {
		t.Errorf("order\n got %s\nwant %s", got, want)
	}
}

func manyHubSessions(n, needs int) []HubSession {
	var out []HubSession
	for i := 0; i < n; i++ {
		st, detail := "done", ""
		if i < needs {
			st, detail = "asked", "question"
		}
		s := hubSession(fmt.Sprintf("s%04d", i), st, detail)
		// ask rows: the lower the index the longer they have waited; done rows: the lower the newer
		if i < needs {
			s.StateSince = tp(hubNow.Add(-time.Duration(1000-i) * time.Minute))
		} else {
			s.StateSince = tp(hubNow.Add(-time.Duration(i) * time.Second))
		}
		out = append(out, s)
	}
	return out
}

// 300 rows and 512 KiB: every needs-you row stays, then the newest.
func TestCaps(t *testing.T) {
	// the needs-you rows are put in the middle and the end of the list
	sessions := manyHubSessions(700, 0)
	for _, i := range []int{3, 350, 699} {
		s := hubSession(fmt.Sprintf("need%d", i), "asked", "question")
		s.StateSince = tp(hubNow.Add(-48 * time.Hour)) // older than everything else
		sessions[i] = s
	}
	v := BuildHub(HubData{Seats: hubSeats(), Sessions: sessions}, hubOptions())
	if err := v.Check(); err != nil {
		t.Fatal(err)
	}
	if len(v.Rows) != MaxRows || v.Truncated != 700-MaxRows {
		t.Fatalf("rows %d truncated %d", len(v.Rows), v.Truncated)
	}
	for _, id := range []string{"need3", "need350", "need699"} {
		if _, ok := v.Rows[id]; !ok {
			t.Errorf("%s dropped", id)
		}
	}
	if len(v.NeedsYou) != 3 || v.Counts.NeedsYou != 3 || v.Counts.Sessions != 700 {
		t.Errorf("needs %v counts %+v", v.NeedsYou, v.Counts)
	}
	// the newest others are the ones kept: s0000 is the newest (0 s old)
	if _, ok := v.Rows["s0000"]; !ok {
		t.Error("newest row dropped")
	}
	if _, ok := v.Rows["s0699"]; ok {
		t.Error("oldest row kept")
	}
	n := 0
	for _, g := range v.Groups {
		n += len(g.Rows)
	}
	if n != MaxRows {
		t.Errorf("groups list %d rows", n)
	}
	raw, _ := json.Marshal(v)
	if len(raw) > MaxBytes {
		t.Errorf("%d bytes", len(raw))
	}
	// the cap does not mark a small view
	if v := BuildHub(HubData{Sessions: manyHubSessions(10, 2)}, hubOptions()); v.Truncated != 0 {
		t.Errorf("truncated %d", v.Truncated)
	}
	if raw, _ := json.Marshal(BuildHub(HubData{Sessions: manyHubSessions(10, 2)}, hubOptions())); strings.Contains(string(raw), "truncated") {
		t.Error("truncated present on a small view")
	}
}

// The byte cap bites before the row cap when rows are large; needs-you rows survive.
func TestByteCap(t *testing.T) {
	big := strings.Repeat("word ", 400)
	var sessions []HubSession
	for i := 0; i < 300; i++ {
		s := hubSession(fmt.Sprintf("s%03d", i), "done", "")
		s.StateSince = tp(hubNow.Add(-time.Duration(i) * time.Second))
		// every text field is cut, so make the rest big: long ids and resume strings
		s.ID = fmt.Sprintf("s%03d-%s", i, strings.Repeat("x", 60))
		s.Resume = sp(big)
		s.Title = sp(big)
		s.Project = sp(big)
		s.LastMessage = sp(big)
		s.MachineName = sp(big)
		sessions = append(sessions, s)
	}
	n := hubSession("need-"+strings.Repeat("y", 40), "asked", "question")
	n.StateSince = tp(hubNow.Add(-72 * time.Hour))
	sessions = append(sessions, n)
	v := BuildHub(HubData{Seats: hubSeats(), Sessions: sessions}, hubOptions())
	raw, _ := json.Marshal(v)
	if len(raw) > MaxBytes {
		t.Fatalf("%d bytes", len(raw))
	}
	if _, ok := v.Rows[n.ID]; !ok {
		t.Error("needs-you row dropped by the byte cap")
	}
	if v.Counts.Sessions != 301 {
		t.Errorf("counts %+v", v.Counts)
	}
}

// More than 300 needs-you rows: the longest waiting 300 stay.
func TestCapsAllNeedYou(t *testing.T) {
	v := BuildHub(HubData{Sessions: manyHubSessions(350, 350)}, hubOptions())
	if len(v.Rows) != MaxRows || v.Counts.NeedsYou != 350 || v.Truncated != 50 {
		t.Fatalf("%d rows, %+v, truncated %d", len(v.Rows), v.Counts, v.Truncated)
	}
	if _, ok := v.Rows["s0000"]; !ok {
		t.Error("longest waiting row dropped")
	}
}

func TestMergeOffline(t *testing.T) {
	local := func(id, st string) HubSession {
		s := hubSession(id, st, "")
		return s
	}
	gpu := hubSession("remote-ask", "asked", "question")
	gpu.Machine, gpu.MachineName = sp("m_gpu"), sp("gpu-box")
	gpu.StateSince = tp(hubNow.Add(-20 * time.Minute))
	gpuDone := hubSession("remote-done", "done", "")
	gpuDone.Machine, gpuDone.MachineName = sp("m_gpu"), sp("gpu-box")
	hubLocalCopy := local("here-1", "working") // the hub's stale copy of this machine's session
	data := HubData{
		Overview: HubOverview{Machines: []HubMachine{{ID: "m_laptop"}, {ID: "m_gpu"}}},
		Seats:    hubSeats(), Sessions: []HubSession{gpu, gpuDone, hubLocalCopy},
	}
	last := BuildHub(data, hubOptions())

	// the live local view: here-1 is now done, here-2 is new. Local seat ids differ from the
	// hub's; the label "work" ties them.
	lo := fixtureOptions(t)
	lo.Options.Machine = Machine{ID: "m_laptop", Name: "laptop"}
	lo.Options.Now = hubNow
	lv, err := BuildLocal(lo)
	if err != nil {
		t.Fatal(err)
	}
	lv.Rows = map[string]Row{}
	lv.Groups = []Group{{Seat: "slot:~/.claude", Label: "work", Colour: "#f59e0b", Rows: []string{"here-1", "here-2"}}}
	for _, id := range []string{"here-1", "here-2"} {
		lv.Rows[id] = Row{ID: id, State: "done", Glyph: GlyphDone, Machine: "m_laptop", Seat: "slot:~/.claude", Since: hubNow.Add(-time.Minute), Here: true}
	}
	since := hubNow.Add(-5 * time.Minute)
	o := hubOptions()
	m := MergeOffline(last, lv, since, o)
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	if m.Source != SourceMixed || m.Hub.State != HubOffline || m.Hub.OfflineSince == nil || !m.Hub.OfflineSince.Equal(since) {
		t.Errorf("header %s %+v", m.Source, m.Hub)
	}
	if !m.Rows["remote-ask"].Greyed || !m.Rows["remote-done"].Greyed {
		t.Error("other machine's rows not greyed")
	}
	if m.Rows["here-1"].Greyed || m.Rows["here-1"].State != "done" || m.Rows["here-2"].Greyed {
		t.Errorf("local rows %+v", m.Rows["here-1"])
	}
	if len(m.Rows) != 4 {
		t.Errorf("%d rows", len(m.Rows))
	}
	// the local rows joined the hub's "work" group, not a second one
	if len(m.Groups) < 2 || m.Groups[0].Label != "work" || fmt.Sprint(m.Groups[0].Rows) != "[remote-ask here-1 here-2 remote-done]" {
		t.Errorf("groups %+v", m.Groups)
	}
	for _, g := range m.Groups {
		if g.Label == "work" && g.Seat != "s_work" {
			t.Errorf("duplicate work group %+v", g)
		}
	}
	// the remote row needs you, and stays in needs_you (greyed)
	if fmt.Sprint(m.NeedsYou) != "[remote-ask]" {
		t.Errorf("needs_you %v", m.NeedsYou)
	}
	// inputs are not modified
	if last.Rows["remote-ask"].Greyed || last.Hub.State != HubLive {
		t.Error("MergeOffline modified its input")
	}
}

func TestWindowsAndGroupsOrder(t *testing.T) {
	// two seats, rows listed in the sessions order inside their group
	a := hubSession("a", "done", "")
	b := hubSession("b", "done", "")
	b.Seat = sp("s_home")
	c := hubSession("c", "working", "")
	v := BuildHub(HubData{Seats: hubSeats(), Sessions: []HubSession{c, b, a}}, hubOptions())
	if fmt.Sprint(v.Groups[0].Rows) != "[c a]" || fmt.Sprint(v.Groups[1].Rows) != "[b]" {
		t.Errorf("%v %v", v.Groups[0].Rows, v.Groups[1].Rows)
	}
}
