package hubsrc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/termenv"
)

// The end-to-end test: the real `wy` binary runs a hub on a free loopback port
// in a temp WY_HOME; records reach it through `wy outbox append` + `wy flush` (this machine)
// and POST /v1/ingest as a second machine; `wy board connect` makes the board token; the hub
// source reads it. Everything runs with a temp HOME, WY_HOME and CLAUDE_CONFIG_DIR, and no
// WY_* / XDG_* from the real environment.
//
//	go test ./internal/board/hubsrc -run E2E     (skipped with -short and on Windows)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type rig struct {
	t      *testing.T
	wy     string
	env    []string
	state  string
	addr   string
	base   string
	hub    *exec.Cmd
	hubLog *syncBuf
	done   chan struct{}
	me     string // this machine's id
}

func buildSy(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(strings.TrimSpace(string(out)))
	bin := filepath.Join(dir, "bin", "wy")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/wy")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wy: %v\n%s", err, b)
	}
	return bin
}

func cleanEnv(tmp string) []string {
	// Built from nothing but PATH: no HOME, XDG_*, WY_* or CLAUDE_* of the real environment
	// can reach the binary (and the terminal packages never read the process environment
	// themselves, internal/termenv/lint_test.go).
	return []string{
		"PATH=" + termenv.FromOS().Lookup("PATH"),
		"HOME=" + filepath.Join(tmp, "home"), "WY_HOME=" + filepath.Join(tmp, "wy"),
		"CLAUDE_CONFIG_DIR=" + filepath.Join(tmp, "claude"), "TZ=UTC", "NO_COLOR=1",
	}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end: builds and runs wy")
	}
	if runtime.GOOS == "windows" {
		t.Skip("end-to-end: unix only")
	}
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, wy: buildSy(t, tmp), env: cleanEnv(tmp), state: filepath.Join(tmp, "wy", "state")}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.addr = ln.Addr().String()
	ln.Close()
	r.base = "http://" + r.addr
	t.Cleanup(func() { r.killHub() })
	r.startHub()

	var info struct {
		Machine struct{ ID string } `json:"machine"`
	}
	if err := json.Unmarshal([]byte(r.run("desktop", "info", "--json")), &info); err != nil || info.Machine.ID == "" {
		t.Fatalf("desktop info: %v", err)
	}
	r.me = info.Machine.ID
	r.run("config", "set", "hub.url", r.base)
	var tok struct{ Token string }
	if err := json.Unmarshal([]byte(r.run("hub", "token", "create", "--kind", "machine", "--name", "laptop", "--machine", r.me, "--json")), &tok); err != nil || tok.Token == "" {
		t.Fatalf("machine token: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.state, "token"), []byte(tok.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) startHub() {
	r.t.Helper()
	cmd := exec.Command(r.wy, "hub", "serve", "--listen", r.addr, "--no-push", "--no-pull")
	cmd.Env = r.env
	hubLog := &syncBuf{}
	r.hubLog = hubLog
	pr, pw := io.Pipe()
	cmd.Stderr, cmd.Stdout = pw, hubLog
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.hub = cmd
	done := make(chan struct{})
	r.done = done
	go func() { _ = cmd.Wait(); pw.Close(); close(done) }()
	up := make(chan struct{}, 1)
	listening := regexp.MustCompile(`listening on http://`)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			fmt.Fprintln(hubLog, sc.Text())
			if listening.MatchString(sc.Text()) {
				select {
				case up <- struct{}{}:
				default:
				}
			}
		}
	}()
	select {
	case <-up:
	case <-done:
		r.t.Fatalf("hub exited:\n%s", hubLog)
	case <-time.After(30 * time.Second):
		r.t.Fatalf("hub did not start:\n%s", hubLog)
	}
}

// killHub is a crash: SIGKILL, no graceful close.
func (r *rig) killHub() {
	if r.hub == nil || r.hub.Process == nil {
		return
	}
	_ = r.hub.Process.Kill()
	<-r.done
	r.hub = nil
}

func (r *rig) run(args ...string) string {
	r.t.Helper()
	return r.runIn("", args...)
}

func (r *rig) runIn(stdin string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command(r.wy, args...)
	cmd.Env = r.env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("wy %s: %v\n%s%s", strings.Join(args, " "), err, out.String(), errb.String())
	}
	return out.String()
}

var e2eSeat = &record.Seat{Account: "acc-e2e", Org: "org-e2e", Email: "e2e@example.com"}

func (r *rig) hookRec(machine string, term *record.Term, sid, event string, fields map[string]any) *record.Record {
	r.t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + sid + ".jsonl"}
	for k, v := range fields {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	rec, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: "~/.claude", Seat: e2eSeat, Term: term, Client: "wy/e2e", Now: time.Now()},
		raw, record.HookOptions{LastMessages: true})
	if err != nil {
		r.t.Fatal(err)
	}
	return rec
}

// viaOutbox is how this machine's records travel: wy outbox append, then wy flush.
func (r *rig) viaOutbox(recs ...*record.Record) {
	r.t.Helper()
	var in bytes.Buffer
	for _, rec := range recs {
		b, err := rec.Encode()
		if err != nil {
			r.t.Fatal(err)
		}
		in.Write(b)
		in.WriteByte('\n')
	}
	r.runIn(in.String(), "outbox", "append")
	r.run("flush")
}

// asMachine posts records as another machine, through /v1/ingest with its own machine token.
func (r *rig) asMachine(machine, token string, recs ...*record.Record) {
	r.t.Helper()
	var raws []json.RawMessage
	for _, rec := range recs {
		b, err := rec.Encode()
		if err != nil {
			r.t.Fatal(err)
		}
		raws = append(raws, b)
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "client": "wy/e2e", "records": raws})
	req, _ := http.NewRequest("POST", r.base+"/v1/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || bytes.Contains(out, []byte(`"rejected":[{`)) {
		r.t.Fatalf("ingest as %s: %d %s", machine, resp.StatusCode, out)
	}
}

func (r *rig) get(token, path string, into any) {
	r.t.Helper()
	req, _ := http.NewRequest("GET", r.base+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		r.t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, into); err != nil {
		r.t.Fatalf("GET %s: %v", path, err)
	}
}

// recTransport records what the source asks of the hub.
type recTransport struct {
	mu   sync.Mutex
	base http.RoundTripper
	reqs []reqLog
}

func (rt *recTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.reqs = append(rt.reqs, reqLog{Path: req.URL.Path, Query: req.URL.RawQuery, LastEventID: req.Header.Get("Last-Event-ID")})
	rt.mu.Unlock()
	return rt.base.RoundTrip(req)
}

func (rt *recTransport) count(path string) int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	n := 0
	for _, r := range rt.reqs {
		if r.Path == path {
			n++
		}
	}
	return n
}

func (rt *recTransport) last(path string) reqLog {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var out reqLog
	for _, r := range rt.reqs {
		if r.Path == path {
			out = r
		}
	}
	return out
}

const (
	sidMine1  = "11111111-aaaa-4aaa-8aaa-000000000001"
	sidMine2  = "11111111-aaaa-4aaa-8aaa-000000000002"
	sidOther1 = "22222222-bbbb-4bbb-8bbb-000000000001"
	focusMine = "warp://session/0123456789abcdef0123456789abcdef"
	focusOth  = "warp://session/fedcba9876543210fedcba9876543210"
)

type errorfer interface {
	Helper()
	Errorf(format string, args ...any)
}

// probe collects sameAsHub's complaints instead of failing the test.
type probe struct{ bad []string }

func (p *probe) Helper()                   {}
func (p *probe) Errorf(f string, a ...any) { p.bad = append(p.bad, fmt.Sprintf(f, a...)) }

// waitSame waits until a View equals the hub's reads (events and refetches settle within a
// few hundred milliseconds) and returns it; it fails with the last differences otherwise.
func waitSame(t *testing.T, r *rig, token string, views <-chan board.View, cur board.View) board.View {
	t.Helper()
	last := cur
	var bad []string
	deadline := time.After(5 * time.Second)
	check := func(v board.View) bool {
		p := &probe{}
		sameAsHub(p, r, token, v)
		bad = p.bad
		return len(p.bad) == 0
	}
	if check(cur) {
		return cur
	}
	for {
		select {
		case v, open := <-views:
			if !open {
				t.Fatal("views closed")
			}
			last = v
			if check(v) {
				return v
			}
		case <-time.After(300 * time.Millisecond):
			if check(last) {
				return last
			}
		case <-deadline:
			t.Fatalf("the View never matched the hub's reads; last differences: %q\nlast: %+v", bad, last.Counts)
		}
	}
}

// sameAsHub checks a View against the hub's own /v1/sessions and /v1/accounts: the ids and
// their order (needs-you list, groups in account order, rows in session order), states,
// questions and seat labels and colours.
func sameAsHub(t errorfer, r *rig, token string, v board.View) {
	t.Helper()
	var ss struct {
		Sessions []wireSession `json:"sessions"`
	}
	r.get(token, "/v1/sessions?limit=300", &ss)
	var as struct {
		Accounts []wireSeat `json:"accounts"`
	}
	r.get(token, "/v1/accounts", &as)

	var needs, order []string
	seatOf := map[string]string{}
	for _, s := range ss.Sessions {
		if s.NeedsYou {
			needs = append(needs, s.ID)
		}
		seatOf[s.ID] = str(s.Seat)
		order = append(order, s.ID)
	}
	if got := strings.Join(v.NeedsYou, ","); got != strings.Join(needs, ",") {
		t.Errorf("needs_you = %s, hub = %s", got, strings.Join(needs, ","))
	}
	// Rows in group order, groups in account order, equal the sessions in hub order grouped
	// by seat.
	var wantGroups []string
	labelOf := map[string]string{}
	for _, a := range as.Accounts {
		var ids []string
		for _, id := range order {
			if seatOf[id] == a.ID {
				ids = append(ids, id)
			}
		}
		// An account the hub lists with no label shows its email, else "account" (board.BuildHub).
		label := str(a.Label)
		if label == "" {
			label = str(a.Email)
		}
		if label == "" {
			label = "account"
		}
		labelOf[a.ID] = label
		wantGroups = append(wantGroups, a.ID+"="+label+"="+strings.Join(ids, ","))
	}
	var gotGroups []string
	for _, g := range v.Groups {
		if g.Label == board.UnattributedLabel {
			continue
		}
		gotGroups = append(gotGroups, g.Seat+"="+g.Label+"="+strings.Join(g.Rows, ","))
	}
	if strings.Join(gotGroups, ";") != strings.Join(wantGroups, ";") {
		t.Errorf("groups:\n got %v\nwant %v", gotGroups, wantGroups)
	}
	for _, g := range v.Groups {
		for _, a := range as.Accounts {
			if a.ID == g.Seat && a.Colour != nil && g.Colour != *a.Colour {
				t.Errorf("seat %s colour %s, hub %s", a.ID, g.Colour, *a.Colour)
			}
		}
	}
	if len(v.Rows) != len(ss.Sessions) {
		t.Errorf("%d rows, hub lists %d sessions", len(v.Rows), len(ss.Sessions))
	}
	for _, s := range ss.Sessions {
		row, ok := v.Rows[s.ID]
		if !ok {
			t.Errorf("row %s missing", s.ID)
			continue
		}
		if row.State != s.State || row.NeedsYou != s.NeedsYou || row.Detail != str(s.Detail) {
			t.Errorf("%s: %s/%s/%v, hub %s/%s/%v", s.ID, row.State, row.Detail, row.NeedsYou, s.State, str(s.Detail), s.NeedsYou)
		}
		if s.State == "asked" && str(s.Question) != "" && row.Line2 != board.Cut(board.Line(str(s.Question)), 200) {
			t.Errorf("%s: question %q, hub %q", s.ID, row.Line2, str(s.Question))
		}
		want := str(s.SeatLabel)
		if want == "" {
			want = labelOf[str(s.Seat)]
		}
		if row.Account != board.Cut(board.Line(want), 32) {
			t.Errorf("%s: account %q, hub %q", s.ID, row.Account, str(s.SeatLabel))
		}
		if s.SeatColour != nil && row.Colour != *s.SeatColour {
			t.Errorf("%s: colour %q, hub %q", s.ID, row.Colour, *s.SeatColour)
		}
	}
}

func TestE2ERealHub(t *testing.T) {
	r := newRig(t)

	// ---- the board token, through the real `wy board connect` (local admin channel).
	out := r.run("board", "connect", "--json")
	var conn struct{ Device, Hub string }
	if err := json.Unmarshal([]byte(out), &conn); err != nil || conn.Hub != r.base || conn.Device == "" {
		t.Fatalf("board connect: %q %v", out, err)
	}
	tok, err := board.LoadToken(r.state, r.base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, tok.Token) {
		t.Fatal("board connect printed the token")
	}

	rt := &recTransport{base: http.DefaultTransport.(*http.Transport).Clone()}
	loc := newFakeLocal()
	var logMu sync.Mutex
	var logs []string
	unpaired := make(chan struct{}, 1)
	src, err := New(Options{
		URL: r.base, Token: tok.Token, Transport: rt, Machine: board.Machine{ID: r.me, Name: "laptop"},
		StateDir: r.state, Local: loc, Loc: time.UTC,
		Backoff: func(n int) time.Duration { return time.Duration(100*(n+1)) * time.Millisecond },
		Logf:    func(f string, a ...any) { logMu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); logMu.Unlock() },
		OnUnpaired: func() {
			_, _ = board.RemoveToken(r.state)
			unpaired <- struct{}{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	views, err := src.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	empty := waitView(t, views, 10*time.Second, isLive)
	if len(empty.Rows) != 0 || rt.count("/v1/overview") != 1 {
		t.Fatalf("empty hub: %d rows, %d overviews", len(empty.Rows), rt.count("/v1/overview"))
	}

	// ---- restart the hub: the board resumes with Last-Event-ID and the hub sends no reset
	// (nothing had been published, and the hub's event ids are time-based, so the id the
	// board holds is still valid on the restarted hub).
	// The live View is posted once the reads are done; the stream request follows it.
	for deadline := time.Now().Add(5 * time.Second); rt.count("/v1/stream") == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	firstID := strings.TrimPrefix(rt.last("/v1/stream").Query, "last_event_id=")
	r.killHub()
	waitView(t, views, 15*time.Second, isOffline)
	r.startHub()
	waitView(t, views, 15*time.Second, isLive)
	if rs := rt.last("/v1/stream"); rs.LastEventID != firstID || firstID == "" {
		t.Errorf("resumed with Last-Event-ID %q, want %q (the stream_id of the first overview)", rs.LastEventID, firstID)
	}
	// The restarted hub may still answer the resume with `reset: hub_restarted` (its broker
	// starts empty), and the board then refetches once; it must never loop.
	if n := rt.count("/v1/overview"); n < 1 || n > 2 {
		t.Errorf("%d overview fetches after a restart; want 1, or 2 after a reset", n)
	}

	// ---- records: this machine through the outbox and flush, another machine over HTTP.
	termMine := &record.Term{App: "warp", ID: "tab1", FocusURL: focusMine}
	r.viaOutbox(
		r.hookRec(r.me, termMine, sidMine1, "UserPromptSubmit", map[string]any{"prompt": "go"}),
		r.hookRec(r.me, nil, sidMine2, "SessionStart", map[string]any{"source": "startup"}),
		r.hookRec(r.me, nil, sidMine2, "Stop", map[string]any{"last_assistant_message": "All done here."}),
	)
	other := "m_" + record.NewID(time.Now())
	var otok struct{ Token string }
	if err := json.Unmarshal([]byte(r.run("hub", "token", "create", "--kind", "machine", "--name", "gpu-box", "--machine", other, "--json")), &otok); err != nil {
		t.Fatal(err)
	}
	r.asMachine(other, otok.Token, r.hookRec(other, &record.Term{App: "warp", ID: "t9", FocusURL: focusOth}, sidOther1, "UserPromptSubmit", map[string]any{"prompt": "go"}))
	v := waitView(t, views, 5*time.Second, func(v board.View) bool { return len(v.Rows) == 3 && v.Rows[sidMine1].State == "working" })
	if v.Rows[sidOther1].Here || !v.Rows[sidMine1].Here {
		t.Errorf("here: mine %v other %v", v.Rows[sidMine1].Here, v.Rows[sidOther1].Here)
	}

	// ---- a Stop with a question becomes `asked` in the stream within 2 s.
	start := time.Now()
	r.viaOutbox(r.hookRec(r.me, termMine, sidMine1, "Stop", map[string]any{
		"last_assistant_message": "Done with the schema.\n\nShould I also update the migration?"}))
	v = waitView(t, views, 2*time.Second, func(v board.View) bool { return v.Rows[sidMine1].State == "asked" })
	t.Logf("flush to asked row: %v", time.Since(start))
	if r1 := v.Rows[sidMine1]; r1.Detail != "question" || r1.Line2Kind != board.Line2Question || !strings.Contains(r1.Line2, "update the migration") {
		t.Errorf("asked row: %+v", r1)
	}

	// ---- focus_url: this machine's session yes, another machine's no.
	if !v.Rows[sidMine1].CanFocus {
		t.Errorf("this machine's session has no can_focus: %+v", v.Rows[sidMine1])
	}
	if v.Rows[sidOther1].CanFocus {
		t.Error("another machine's focus URL reached the board")
	}

	// ---- the View equals the hub's reads, from the stream and after a full refetch.
	v = waitSame(t, r, tok.Token, views, v)
	src.Refresh()
	v = waitView(t, views, 3*time.Second, func(v board.View) bool { return rt.count("/v1/overview") >= 2 })
	v = waitSame(t, r, tok.Token, views, v)

	// ---- kill the hub: offline; local rows live, other machines greyed.
	lv := board.View{
		V: 1, Source: board.SourceLocal, Hub: board.HubInfo{State: board.HubNone}, Machine: board.Machine{ID: r.me, Name: "laptop"},
		NeedsYou: []string{},
		Groups:   []board.Group{{Seat: v.Rows[sidMine2].Seat, Label: v.Rows[sidMine2].Account, Rows: []string{sidMine2}}},
		Rows: map[string]board.Row{sidMine2: {ID: sidMine2, State: "working", Glyph: board.GlyphWorking, Status: "9s",
			Machine: r.me, Here: true, Seat: v.Rows[sidMine2].Seat, Account: v.Rows[sidMine2].Account, Since: time.Now()}},
		Counts: board.Counts{Sessions: 1, Machines: 1},
	}
	loc.ch <- lv
	r.killHub()
	off := waitView(t, views, 15*time.Second, func(v board.View) bool { return isOffline(v) && v.Rows[sidMine2].State == "working" })
	if off.Source != board.SourceMixed || off.Hub.OfflineSince == nil {
		t.Errorf("offline view: source %s since %v", off.Source, off.Hub.OfflineSince)
	}
	if r1 := off.Rows[sidOther1]; !r1.Greyed {
		t.Errorf("another machine's row is not greyed: %+v", r1)
	}
	if r1 := off.Rows[sidMine2]; r1.Greyed || r1.State != "working" {
		t.Errorf("local row not live: %+v", r1)
	}
	if _, ok := off.Rows[sidMine1]; ok {
		t.Error("this machine's stale hub row stayed while the local fold is live")
	}
	if err := off.Check(); err != nil {
		t.Error(err)
	}

	// ---- restart: live again. The hub's stream ids start over, so the old Last-Event-ID is
	// beyond the new hub's: it answers `reset` and the board refetches.
	r.startHub()
	back := waitView(t, views, 20*time.Second, func(v board.View) bool { return isLive(v) && len(v.Rows) == 3 })
	back = waitSame(t, r, tok.Token, views, back)
	if rs := rt.last("/v1/stream"); rs.LastEventID == "" {
		t.Error("reconnected without Last-Event-ID")
	}

	// ---- revoke the device: unpaired.
	r.run("hub", "token", "revoke", conn.Device, "--yes")
	src.Refresh()
	un := waitView(t, views, 10*time.Second, func(v board.View) bool { return v.Hub.State == board.HubUnpaired })
	if un.Source != board.SourceLocal {
		t.Errorf("unpaired source %s", un.Source)
	}
	select {
	case <-unpaired:
	case <-time.After(2 * time.Second):
		t.Error("OnUnpaired not called")
	}
	if _, err := board.LoadToken(r.state, r.base); err != board.ErrNoToken {
		t.Errorf("token still usable after 401: %v", err)
	}

	// ---- the token never reached the logs or the Views.
	logMu.Lock()
	defer logMu.Unlock()
	for _, l := range logs {
		if strings.Contains(l, tok.Token) {
			t.Errorf("token in a log line: %s", l)
		}
	}
	for _, vv := range []board.View{empty, v, off, back, un} {
		b, _ := json.Marshal(vv)
		if bytes.Contains(b, []byte(tok.Token)) {
			t.Error("token in a View")
		}
	}
}
