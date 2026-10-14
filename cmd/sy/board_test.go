package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/term"

	"github.com/mraj602/switchyard/internal/board"
)

// isolateBoard points every folder variable into a temp dir (nothing real is read or written).
func isolateBoard(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "SY_CONFIG_DIR", "SY_STATE_DIR", "SY_DATA_DIR", "COLUMNS", "NO_COLOR", "COLORTERM"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SY_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("TERM", "xterm")
}

type fakeBoardSource struct {
	views   []board.View
	gap     time.Duration
	refresh atomic.Int32
	sel     boardSelection
}

func (f *fakeBoardSource) Start(ctx context.Context) (<-chan board.View, error) {
	ch := make(chan board.View)
	go func() {
		defer close(ch)
		for _, v := range f.views {
			select {
			case ch <- v:
			case <-ctx.Done():
				return
			}
			time.Sleep(f.gap)
		}
	}()
	return ch, nil
}
func (f *fakeBoardSource) Refresh() { f.refresh.Add(1) }

// useSource installs a fake source and terminal seams that fail the test on any mode change.
func useSource(t *testing.T, src *fakeBoardSource) {
	t.Helper()
	oldSel, oldRaw, oldRestore := boardSelectSource, boardMakeRaw, boardRestore
	boardSelectSource = func(s boardSelection) (board.Source, error) { src.sel = s; return src, nil }
	boardMakeRaw = func(*os.File) (*term.State, error) { t.Error("raw mode entered"); return nil, errors.New("no") }
	boardRestore = func(*os.File, *term.State) error { t.Error("terminal restored"); return nil }
	t.Cleanup(func() { boardSelectSource, boardMakeRaw, boardRestore = oldSel, oldRaw, oldRestore })
}

func tinyView(n int) board.View {
	r := func(id, proj string) board.Row {
		return board.Row{ID: id, State: "working", Glyph: board.GlyphWorking, Status: "2m", Project: proj, Title: "t", Account: "work", Colour: "#f59e0b",
			Here: true, ContextLevel: board.ContextOK, Age: "2m", Line2Kind: board.Line2None, Resume: "claude --resume " + id}
	}
	v := board.View{V: board.Version, GeneratedAt: time.Date(2026, 10, 20, 14, 6, 0, 0, time.UTC), Source: board.SourceLocal,
		Hub: board.HubInfo{State: board.HubNone}, Machine: board.Machine{ID: "m", Name: "laptop"},
		Counts: board.Counts{Sessions: 2 + n, Machines: 1}, NeedsYou: []string{},
		Rows: map[string]board.Row{"a": r("a", "api"), "b": r("b", "blog")}}
	v.Groups = []board.Group{{Seat: "s", Label: "work", Colour: "#f59e0b", Rows: []string{"a", "b"}}}
	return v
}

func runBoardCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := dispatch(append([]string{"board"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestBoardOnceText(t *testing.T) {
	isolateBoard(t)
	src := &fakeBoardSource{views: []board.View{tinyView(0)}}
	useSource(t, src)
	code, out, errb := runBoardCmd(t, "--once", "--cols", "80", "--session", "b")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if !strings.Contains(out, "Switchyard  2 sessions · 1 machines · local") || !strings.Contains(out, "api · t") || strings.Contains(out, "\x1b") {
		t.Errorf("frame:\n%s", out)
	}
	if src.sel.Mode != "auto" || src.sel.Session != "b" || src.sel.StateDir == "" {
		t.Errorf("selection %+v", src.sel)
	}
}

func TestBoardOnceJSON(t *testing.T) {
	isolateBoard(t)
	useSource(t, &fakeBoardSource{views: []board.View{tinyView(0)}})
	code, out, errb := runBoardCmd(t, "--once", "--json", "--session", "b")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	v, err := board.Decode([]byte(out))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !v.Rows["b"].ThisSession || v.Rows["a"].ThisSession {
		t.Errorf("this_session not set on b only")
	}
}

// --once must not enter raw mode or touch the console (the seams fail the test if they run).
func TestBoardOnceLeavesTerminalAlone(t *testing.T) {
	isolateBoard(t)
	useSource(t, &fakeBoardSource{views: []board.View{tinyView(0)}})
	for _, args := range [][]string{{"--once"}, {"--once", "--json"}, {}} { // {} is the non-tty line fallback
		if code, _, errb := runBoardCmd(t, args...); code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errb)
		}
	}
}

func TestBoardFlagsAndExitCodes(t *testing.T) {
	isolateBoard(t)
	src := &fakeBoardSource{views: []board.View{tinyView(0)}}
	useSource(t, src)
	for _, args := range [][]string{
		{"--json"}, {"--follow"}, {"--ndjson"}, {"--follow", "--once", "--ndjson"}, {"--source", "x"},
		{"--filter", "bogus"}, {"--filter", "machine:"}, {"--cols", "5"}, {"extra"}, {"--nope"},
	} {
		if code, _, _ := runBoardCmd(t, args...); code != 2 {
			t.Errorf("sy board %v: exit %d, want 2", args, code)
		}
	}
	for _, f := range []string{"all", "needs", "here", "machine:gpu-box", "NEEDS"} {
		if code, _, errb := runBoardCmd(t, "--once", "--filter", f); code != 0 {
			t.Errorf("--filter %s: %d %s", f, code, errb)
		}
	}
	if code, _, errb := runBoardCmd(t, "--once", "--source", "local"); code != 0 || src.sel.Mode != "local" {
		t.Errorf("exit %d %s mode %q", code, errb, src.sel.Mode)
	}
	for err, want := range map[error]int{errBoardNoHub: 4, errBoardHubDown: 5} {
		e := err
		boardSelectSource = func(boardSelection) (board.Source, error) { return nil, e }
		if code, _, _ := runBoardCmd(t, "--source", "hub", "--once"); code != want {
			t.Errorf("%v: exit %d, want %d", err, code, want)
		}
	}
}

func TestBoardNDJSONThroughCommand(t *testing.T) {
	isolateBoard(t)
	old := boardNDJSON
	boardNDJSON = ndjsonTiming{debounce: 30 * time.Millisecond, heartbeat: time.Hour}
	defer func() { boardNDJSON = old }()
	views := []board.View{tinyView(0), tinyView(1), tinyView(2)}
	same := tinyView(2)
	same.GeneratedAt = same.GeneratedAt.Add(time.Minute)
	views = append(views, same)
	useSource(t, &fakeBoardSource{views: views, gap: time.Millisecond})
	code, out, errb := runBoardCmd(t, "--follow", "--ndjson")
	if code != 0 {
		t.Fatalf("exit %d %s", code, errb)
	}
	var counts []int
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		v, err := board.Decode(sc.Bytes())
		if err != nil {
			t.Fatalf("line is not a View: %v\n%s", err, sc.Text())
		}
		counts = append(counts, v.Counts.Sessions)
	}
	if len(counts) == 0 || counts[0] != 2 {
		t.Fatalf("lines %v", counts)
	}
	for i := 1; i < len(counts); i++ {
		if counts[i] <= counts[i-1] {
			t.Errorf("lines out of order or repeated: %v", counts)
		}
	}
	if strings.Count(out, "\n") != len(counts) {
		t.Errorf("a View spans lines")
	}
}

func TestFollowNDJSONDebounceAndHeartbeat(t *testing.T) {
	ch := make(chan board.View)
	pr := &lineSink{lines: make(chan string, 20)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- followNDJSON(ctx, ch, pr, ndjsonTiming{debounce: 60 * time.Millisecond, heartbeat: 500 * time.Millisecond})
	}()
	ch <- tinyView(0)
	first := pr.next(t)
	for i := 1; i <= 3; i++ { // a burst: coalesced into one line holding the last View
		ch <- tinyView(i)
	}
	second := pr.next(t)
	var a, b board.View
	if json.Unmarshal([]byte(first), &a) != nil || json.Unmarshal([]byte(second), &b) != nil || a.Counts.Sessions != 2 || b.Counts.Sessions != 5 {
		t.Fatalf("first %s\nsecond %s", first, second)
	}
	select {
	case l := <-pr.lines:
		var h board.View
		if err := json.Unmarshal([]byte(l), &h); err != nil || h.Counts.Sessions != 5 {
			t.Fatalf("heartbeat %s", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no heartbeat")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// a reader that went away ends the stream with its error
	ch2 := make(chan board.View, 1)
	ch2 <- tinyView(0)
	if err := followNDJSON(context.Background(), ch2, failWriter{}, ndjsonTiming{debounce: time.Millisecond, heartbeat: time.Hour}); err == nil {
		t.Error("write error not returned")
	}
}

func TestBoardNDJSONDefaults(t *testing.T) {
	if boardNDJSON.debounce != 250*time.Millisecond || boardNDJSON.heartbeat != 30*time.Second {
		t.Errorf("timings %+v", boardNDJSON)
	}
}

type lineSink struct{ lines chan string }

func (l *lineSink) Write(p []byte) (int, error) {
	l.lines <- strings.TrimRight(string(p), "\n")
	return len(p), nil
}

func (l *lineSink) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-l.lines:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("no line")
		return ""
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
