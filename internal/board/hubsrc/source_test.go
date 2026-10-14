package hubsrc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/board"
)

func waitStreams(t *testing.T, h *fakeHub, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for h.streamCount() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.streamCount() < n {
		t.Fatalf("%d streams, want %d", h.streamCount(), n)
	}
}

func isLive(v board.View) bool    { return v.Hub.State == board.HubLive }
func isOffline(v board.View) bool { return v.Hub.State == board.HubOffline }

func TestLiveViewFromHubReads(t *testing.T) {
	h := newFakeHub(t)
	_, ch, _ := startSource(t, h, nil)
	v := waitView(t, ch, 3*time.Second, isLive)

	if err := v.Check(); err != nil {
		t.Fatal(err)
	}
	if v.Source != board.SourceHub || v.Hub.URL != h.srv.URL || v.Hub.OfflineSince != nil {
		t.Errorf("source/hub: %+v %+v", v.Source, v.Hub)
	}
	if got := strings.Join(v.NeedsYou, ","); got != "c1" {
		t.Errorf("needs_you = %q", got)
	}
	if v.Counts != (board.Counts{Sessions: 3, Machines: 2, NeedsYou: 1}) {
		t.Errorf("counts = %+v", v.Counts)
	}
	if len(v.Groups) != 2 || v.Groups[0].Seat != "s_work" || v.Groups[0].Label != "work" || v.Groups[0].Colour != "#f59e0b" ||
		v.Groups[0].WarpColour != "yellow" || v.Groups[1].WarpColour != "blue" {
		t.Errorf("groups = %+v", v.Groups)
	}
	if g := v.Groups[0]; strings.Join(g.Rows, ",") != "c1,c2" || g.FiveHour == nil || !strings.HasPrefix(g.FiveHour.Text, "41% · ") || !strings.HasSuffix(g.FiveHour.Text, "15:10") || g.SevenDay == nil || g.SevenDay.Text != "30%" {
		t.Errorf("work group = %+v", g)
	}
	r := v.Rows["c1"]
	if r.State != "asked" || r.Status != "asked" || r.Glyph != board.GlyphNeeds || r.Line2 != "Keep the old column?" || r.Line2Kind != board.Line2Question ||
		!r.Here || r.Account != "work" || r.Colour != "#f59e0b" || r.CanFocus {
		t.Errorf("c1 = %+v", r)
	}
	if r := v.Rows["c2"]; r.Here || r.MachineName != "gpu-box" || r.Status != "5m" || r.Glyph != board.GlyphWorking {
		t.Errorf("c2 = %+v", r)
	}

	// T3: overview, then accounts, then sessions?limit=300, then the stream from stream_id.
	waitStreams(t, h, 1)
	var order []string
	h.mu.Lock()
	for _, rq := range h.reqs {
		order = append(order, rq.Path)
	}
	h.mu.Unlock()
	if got, want := strings.Join(order[:4], " "), "/v1/overview /v1/accounts /v1/sessions /v1/stream"; got != want {
		t.Errorf("request order = %s, want %s", got, want)
	}
	if q := h.requests("/v1/sessions")[0].Query; q != "limit=300" {
		t.Errorf("sessions query = %q", q)
	}
	if q := h.requests("/v1/stream")[0].Query; q != "last_event_id=7" {
		t.Errorf("stream query = %q", q)
	}
	h.mu.Lock()
	for _, rq := range h.reqs {
		if rq.Auth != "Bearer "+h.token {
			t.Errorf("%s: Authorization %q", rq.Path, rq.Auth)
		}
		if strings.Contains(rq.Query, "s3cret") {
			t.Errorf("token in the query of %s", rq.Path)
		}
	}
	h.mu.Unlock()
}

func TestSessionEventUpdatesTheView(t *testing.T) {
	h := newFakeHub(t)
	_, ch, _ := startSource(t, h, nil)
	waitView(t, ch, 3*time.Second, isLive)

	c2 := sess("c2", "asked", "question", true, "s_work", "work", "#f59e0b", "m_other", "gpu-box", "web",
		time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC), "Ship it?\x1b]2;pwn\x07")
	c2["focus_url"] = "warp://session/0123456789abcdef0123456789abcdef"
	h.event(8, "session", map[string]any{"session": c2, "change": "state", "previous_state": "working"})

	v := waitView(t, ch, 2*time.Second, func(v board.View) bool { return v.Rows["c2"].State == "asked" })
	if got := strings.Join(v.NeedsYou, ","); got != "c1,c2" {
		t.Errorf("needs_you = %q, want c1,c2 (oldest waiting first)", got)
	}
	if r := v.Rows["c2"]; r.Line2 != "Ship it?" || !r.CanFocus || r.Line2Kind != board.Line2Question {
		t.Errorf("c2 = %+v", r)
	}
	if strings.ContainsAny(v.Rows["c2"].Line2, "\x1b\x07") {
		t.Error("control characters reached the View")
	}

	// An ended session leaves the board.
	end := sess("c3", "ended", "", false, "s_home", "home", "#3b82f6", "m_me", "laptop", "docs", time.Now(), "")
	h.event(9, "session", map[string]any{"session": end, "change": "ended"})
	v = waitView(t, ch, 2*time.Second, func(v board.View) bool { _, ok := v.Rows["c3"]; return !ok })
	if v.Counts.Sessions != 2 {
		t.Errorf("counts = %+v", v.Counts)
	}
}

func TestUsageEventRefetchesAccounts(t *testing.T) {
	h := newFakeHub(t)
	_, ch, _ := startSource(t, h, nil)
	waitView(t, ch, 3*time.Second, isLive)
	h.set(func(h *fakeHub) {
		h.seats[0]["windows"] = []any{map[string]any{"key": "five_hour", "used_pct": 88.0, "resets_at": "2099-01-01T15:10:00.000Z"}}
	})
	h.event(8, "usage", map[string]any{"seat": "s_work"})
	v := waitView(t, ch, 3*time.Second, func(v board.View) bool { return v.Groups[0].FiveHour != nil && v.Groups[0].FiveHour.Pct == 88 })
	if v.Groups[0].SevenDay != nil {
		t.Errorf("seven_day = %+v", v.Groups[0].SevenDay)
	}
}

// A new account appears with its first session: the session event names a seat the board has
// no card for, so /v1/accounts is fetched again (once).
func TestUnknownSeatInSessionEventRefetchesAccounts(t *testing.T) {
	h := newFakeHub(t)
	_, ch, _ := startSource(t, h, nil)
	waitView(t, ch, 3*time.Second, isLive)
	h.set(func(h *fakeHub) {
		h.seats = append(h.seats, map[string]any{"id": "s_new", "label": "fresh", "colour": "#ef4444", "windows": []any{}})
	})
	c4 := sess("c4", "working", "", false, "s_new", "fresh", "#ef4444", "m_me", "laptop", "api", time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC), "")
	h.event(8, "session", map[string]any{"session": c4, "change": "new"})
	v := waitView(t, ch, 3*time.Second, func(v board.View) bool {
		for _, g := range v.Groups {
			if g.Seat == "s_new" && len(g.Rows) == 1 {
				return true
			}
		}
		return false
	})
	if v.Rows["c4"].Account != "fresh" {
		t.Errorf("c4 = %+v", v.Rows["c4"])
	}
	// A second event of the same unknown seat does not fetch again.
	n := len(h.requests("/v1/accounts"))
	h.set(func(h *fakeHub) { h.seats = h.seats[:2] }) // the hub forgets it (hidden): still no refetch storm
	h.event(9, "session", map[string]any{"session": c4, "change": "update"})
	time.Sleep(400 * time.Millisecond)
	if got := len(h.requests("/v1/accounts")); got != n {
		t.Errorf("accounts fetched %d times, want %d", got, n)
	}
}

func TestReconnectResumesWithLastEventID(t *testing.T) {
	h := newFakeHub(t)
	_, ch, _ := startSource(t, h, nil)
	waitView(t, ch, 3*time.Second, isLive)
	h.event(11, "ping", map[string]any{})
	h.event(12, "usage", map[string]any{"seat": "s_work"})
	time.Sleep(100 * time.Millisecond)
	h.dropStreams()

	deadline := time.Now().Add(3 * time.Second)
	for len(h.requests("/v1/stream")) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	rs := h.requests("/v1/stream")
	if len(rs) < 2 {
		t.Fatalf("no reconnect: %+v", rs)
	}
	if rs[1].LastEventID != "12" {
		t.Errorf("reconnect Last-Event-ID = %q, want 12", rs[1].LastEventID)
	}
	if n := len(h.requests("/v1/overview")); n != 1 {
		t.Errorf("%d overview fetches; a resume without reset must not refetch", n)
	}
	// And the board never went offline: one failed reconnect is needed for that.
	select {
	case v := <-ch:
		if isOffline(v) {
			t.Errorf("went offline on a clean resume: %+v", v.Hub)
		}
	default:
	}
}

func TestResetRefetchesEverything(t *testing.T) {
	h := newFakeHub(t)
	_, ch, _ := startSource(t, h, nil)
	waitView(t, ch, 3*time.Second, isLive)
	h.set(func(h *fakeHub) {
		h.sessions = h.sessions[:1]
		h.streamID = 99
	})
	h.push("event: reset\ndata: {\"reason\":\"hub_restarted\"}\n\n")
	v := waitView(t, ch, 3*time.Second, func(v board.View) bool { return v.Counts.Sessions == 1 })
	if len(v.Rows) != 1 {
		t.Errorf("rows = %d", len(v.Rows))
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(h.requests("/v1/stream")) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if rs := h.requests("/v1/stream"); len(rs) < 2 || rs[1].Query != "last_event_id=99" {
		t.Errorf("stream after reset: %+v", rs)
	}
}

func TestRefreshRefetches(t *testing.T) {
	h := newFakeHub(t)
	loc := newFakeLocal()
	s, ch, _ := startSource(t, h, func(o *Options) { o.Local = loc })
	waitView(t, ch, 3*time.Second, isLive)
	h.set(func(h *fakeHub) { h.sessions = h.sessions[:2] })
	s.Refresh()
	waitView(t, ch, 3*time.Second, func(v board.View) bool { return v.Counts.Sessions == 2 })
	loc.mu.Lock()
	defer loc.mu.Unlock()
	if loc.refreshes != 1 {
		t.Errorf("local refreshes = %d", loc.refreshes)
	}
}

// localView is what a local source gives for this machine: one live row for c1 (so the hub's
// copy is replaced) and one only the local fold knows.
func localView() board.View {
	since := time.Date(2026, 10, 20, 14, 7, 0, 0, time.UTC)
	return board.View{
		V: 1, Source: board.SourceLocal, Hub: board.HubInfo{State: board.HubNone}, Machine: board.Machine{ID: "m_me", Name: "laptop"},
		NeedsYou: []string{"l1"},
		Groups:   []board.Group{{Seat: "s_work", Label: "work", Colour: "#f59e0b", Rows: []string{"l1", "c1"}}},
		Rows: map[string]board.Row{
			"l1": {ID: "l1", Seat: "s_work", State: "needs_permission", NeedsYou: true, Glyph: board.GlyphNeeds, Status: "perm", Machine: "m_me", Here: true, Since: since, Account: "work"},
			"c1": {ID: "c1", Seat: "s_work", State: "done", Glyph: board.GlyphDone, Status: "done", Machine: "m_me", Here: true, Since: since.Add(time.Minute), Account: "work"},
		},
		Counts: board.Counts{Sessions: 2, Machines: 1, NeedsYou: 1},
	}
}

func TestOfflineMergesLocalAndGreysOthers(t *testing.T) {
	h := newFakeHub(t)
	loc := newFakeLocal()
	loc.ch <- localView()
	stateDir := t.TempDir()
	_, ch, _ := startSource(t, h, func(o *Options) { o.Local = loc; o.StateDir = stateDir })
	waitView(t, ch, 3*time.Second, isLive)

	h.srv.CloseClientConnections()
	h.dropStreams()
	h.set(func(h *fakeHub) { h.status = 503 })

	v := waitView(t, ch, 3*time.Second, isOffline)
	if v.Source != board.SourceMixed || v.Hub.OfflineSince == nil {
		t.Errorf("source %q offline_since %v", v.Source, v.Hub.OfflineSince)
	}
	if err := v.Check(); err != nil {
		t.Fatal(err)
	}
	if r := v.Rows["c2"]; !r.Greyed || r.Machine != "m_other" {
		t.Errorf("other machine's row: %+v", r)
	}
	if r := v.Rows["l1"]; r.Greyed || r.Status != "perm" {
		t.Errorf("local row: %+v", r)
	}
	if r := v.Rows["c1"]; r.Greyed || r.Status != "done" {
		t.Errorf("this machine's hub row must be replaced by the live local one: %+v", r)
	}
	if r := v.Rows["c3"]; r.Greyed {
		t.Errorf("c3 is this machine's; with a local source it is not in the local fold, so it is gone: %+v", r)
	}
	if _, ok := v.Rows["c3"]; ok {
		t.Error("c3 (this machine, absent locally) stayed")
	}
	if got := strings.Join(v.NeedsYou, ","); got != "l1" {
		t.Errorf("needs_you = %q", got)
	}
	if v.Groups[0].FiveHour == nil || v.Groups[0].FiveHour.Pct != 41 {
		t.Errorf("limit text lost offline: %+v", v.Groups[0])
	}
	// board.MergeOffline orders the mixed rows as the local fold does (status.rank):
	// needs you, then working, then done.
	if strings.Join(v.Groups[0].Rows, ",") != "l1,c2,c1" {
		t.Errorf("work rows = %v", v.Groups[0].Rows)
	}

	// A local change shows while offline.
	lv := localView()
	r := lv.Rows["l1"]
	r.Status = "perm · Bash"
	lv.Rows["l1"] = r
	loc.ch <- lv
	waitView(t, ch, 2*time.Second, func(v board.View) bool { return v.Rows["l1"].Status == "perm · Bash" })

	// last-view.json is 0600 and holds the hub view.
	b, err := os.ReadFile(LastViewFile(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	lastv, err := board.Decode(b)
	if err != nil || lastv.Source != board.SourceHub || len(lastv.Rows) != 3 {
		t.Errorf("last-view.json: %v %+v", err, lastv.Counts)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(LastViewFile(stateDir))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("last-view.json mode %v", fi.Mode().Perm())
		}
	}

	// The hub comes back: live again from the stream, no refetch needed.
	h.set(func(h *fakeHub) { h.status = 0 })
	waitView(t, ch, 3*time.Second, isLive)
}

func TestStartsOfflineFromLastView(t *testing.T) {
	h := newFakeHub(t)
	stateDir := t.TempDir()
	// A first run saves the last view.
	{
		_, ch, cancel := startSource(t, h, func(o *Options) { o.StateDir = stateDir })
		waitView(t, ch, 3*time.Second, isLive)
		// Force a save: going offline saves the pending view.
		h.set(func(h *fakeHub) { h.status = 503 })
		h.dropStreams()
		waitView(t, ch, 3*time.Second, isOffline)
		cancel()
	}
	// A second board starts while the hub is down: other machines show, greyed.
	h.set(func(h *fakeHub) { h.status = 503 })
	_, ch, _ := startSource(t, h, func(o *Options) { o.StateDir = stateDir })
	v := waitView(t, ch, 3*time.Second, isOffline)
	if r, ok := v.Rows["c2"]; !ok || !r.Greyed {
		t.Errorf("c2 from last-view.json: %+v ok=%v", r, ok)
	}

	// A last view made for another hub URL is ignored.
	h2 := newFakeHub(t)
	h2.set(func(h *fakeHub) { h.status = 503 })
	_, ch2, _ := startSource(t, h2, func(o *Options) { o.StateDir = stateDir })
	v2 := waitView(t, ch2, 3*time.Second, isOffline)
	if len(v2.Rows) != 0 {
		t.Errorf("rows from another hub's last view: %v", v2.Rows)
	}
}

func TestUnauthorizedIsUnpaired(t *testing.T) {
	h := newFakeHub(t)
	loc := newFakeLocal()
	loc.ch <- localView()
	var dropped atomic.Int32
	s, ch, _ := startSource(t, h, func(o *Options) { o.Local = loc; o.OnUnpaired = func() { dropped.Add(1) } })
	waitView(t, ch, 3*time.Second, isLive)

	h.set(func(h *fakeHub) { h.status = 401 })
	s.Refresh()
	v := waitView(t, ch, 3*time.Second, func(v board.View) bool { return v.Hub.State == board.HubUnpaired })
	if v.Source != board.SourceLocal || v.Rows["l1"].ID == "" {
		t.Errorf("unpaired view: %+v", v)
	}
	if _, ok := v.Rows["c2"]; ok {
		t.Error("hub rows stayed after 401")
	}
	time.Sleep(100 * time.Millisecond)
	if n := dropped.Load(); n != 1 {
		t.Errorf("OnUnpaired called %d times", n)
	}
	n := len(h.requests("/v1/overview"))
	time.Sleep(150 * time.Millisecond)
	if len(h.requests("/v1/overview")) != n {
		t.Error("kept polling the hub after 401")
	}
}

func TestUnauthorizedOnFirstFetch(t *testing.T) {
	h := newFakeHub(t)
	var dropped atomic.Int32
	_, ch, _ := startSource(t, h, func(o *Options) { o.Token = "revoked"; o.OnUnpaired = func() { dropped.Add(1) } })
	waitView(t, ch, 3*time.Second, func(v board.View) bool { return v.Hub.State == board.HubUnpaired })
	if dropped.Load() != 1 {
		t.Errorf("OnUnpaired = %d", dropped.Load())
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hit atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1) }))
	t.Cleanup(target.Close)
	h := newFakeHub(t)
	h.set(func(h *fakeHub) { h.redirect = target.URL + "/v1/overview" })
	_, ch, _ := startSource(t, h, nil)
	waitView(t, ch, 3*time.Second, isOffline)
	if hit.Load() != 0 {
		t.Errorf("the redirect target got %d requests (and the token)", hit.Load())
	}
}

func TestPingTimeoutReconnects(t *testing.T) {
	h := newFakeHub(t)
	_, ch, _ := startSource(t, h, func(o *Options) { o.PingTimeout = 150 * time.Millisecond })
	waitView(t, ch, 3*time.Second, isLive)
	deadline := time.Now().Add(3 * time.Second)
	for h.streamCount() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.streamCount() < 3 {
		t.Fatalf("a silent stream was not reconnected (%d streams)", h.streamCount())
	}
	// Pings keep it open.
	n := h.streamCount()
	for i := 0; i < 6; i++ {
		h.push("event: ping\ndata: {}\n\n")
		time.Sleep(60 * time.Millisecond)
	}
	if h.streamCount() > n+1 {
		t.Errorf("pings did not hold the stream: %d -> %d", n, h.streamCount())
	}
}

func TestReplyOverCapIsAFailure(t *testing.T) {
	h := newFakeHub(t)
	h.set(func(h *fakeHub) { h.bigReply = true })
	_, ch, _ := startSource(t, h, nil)
	waitView(t, ch, 5*time.Second, isOffline)
}

func TestLogsNeverHoldTheToken(t *testing.T) {
	h := newFakeHub(t)
	var mu sync.Mutex
	var lines []string
	_, ch, _ := startSource(t, h, func(o *Options) {
		o.Logf = func(f string, a ...any) { mu.Lock(); lines = append(lines, f+" "+toString(a)); mu.Unlock() }
	})
	waitView(t, ch, 3*time.Second, isLive)
	h.set(func(h *fakeHub) { h.status = 500 })
	h.dropStreams()
	waitView(t, ch, 3*time.Second, isOffline)
	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 {
		t.Fatal("expected log lines")
	}
	for _, l := range lines {
		if strings.Contains(l, "s3cret") {
			t.Errorf("token in log: %s", l)
		}
	}
}

func toString(a []any) string {
	b, _ := json.Marshal(a)
	return string(b)
}

func TestCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://hub.example":     true,
		"http://127.0.0.1:8787":   true,
		"http://localhost:1":      true,
		"http://[::1]:8787":       true,
		"http://hub.lan:8787":     false,
		"http://10.0.0.5":         false,
		"ftp://hub.example":       false,
		"https://u:p@hub.example": false,
		"hub.example":             false,
	} {
		_, err := CheckURL(raw, false)
		if (err == nil) != ok {
			t.Errorf("CheckURL(%q, false): %v, want ok=%v", raw, err, ok)
		}
	}
	if _, err := CheckURL("http://hub.lan:8787", true); err != nil {
		t.Errorf("accepted LAN URL refused: %v", err)
	}
}

func TestStartTwiceFails(t *testing.T) {
	h := newFakeHub(t)
	s, _, _ := startSource(t, h, nil)
	if _, err := s.Start(t.Context()); err == nil {
		t.Error("second Start succeeded")
	}
}

func TestLastViewRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if LoadLastView(dir, "https://x.example") != nil {
		t.Fatal("empty dir")
	}
	v := localView()
	v.Hub = board.HubInfo{State: board.HubLive, URL: "https://x.example"}
	if err := SaveLastView(dir, v); err != nil {
		t.Fatal(err)
	}
	if LoadLastView(dir, "https://x.example/") == nil {
		t.Error("not loaded")
	}
	if LoadLastView(dir, "https://y.example") != nil {
		t.Error("loaded for another hub")
	}
	if err := os.WriteFile(filepath.Join(dir, "board", "last-view.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if LoadLastView(dir, "https://x.example") != nil {
		t.Error("loaded garbage")
	}
}
