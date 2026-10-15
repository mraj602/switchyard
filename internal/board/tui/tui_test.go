package tui

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/board"
	"github.com/mraj602/switchyard/internal/termenv"
)

var update = flag.Bool("update", false, "rewrite the golden frames")

func TestGoldenFrames(t *testing.T) {
	for _, cols := range []int{40, 60, 100, 160} {
		o := Options{Mode: ModeTrue}
		m := newModel(&o)
		m.v, m.have, m.cursor = fixtureView(), true, idAsked
		m.settle()
		lines := m.frame(cols, 0, true)
		for i, l := range lines {
			if w := visibleWidth(l); w > cols {
				t.Errorf("cols %d line %d is %d wide: %q", cols, i, w, l)
			}
		}
		got := strings.Join(lines, "\n") + "\n"
		file := filepath.Join("testdata", "frame-"+strconv.Itoa(cols)+".golden")
		if *update {
			if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("frame at %d columns differs from %s (go test -update rewrites it):\n%s", cols, file, got)
		}
	}
}

var sgrRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func visibleWidth(s string) int { return strWidth(sgrRE.ReplaceAllString(s, "")) }

func TestLayoutChoice(t *testing.T) {
	o := Options{Mode: ModePlain}
	m := newModel(&o)
	m.v, m.have = fixtureView(), true
	two := func(cols int) bool {
		return strings.Contains(strings.Join(m.frame(cols, 0, false), "\n"), "Update the migration")
	}
	if two(60) || !two(61) {
		t.Errorf("full layout must start above 60 columns")
	}
	o.Compact = true
	if two(160) {
		t.Errorf("--compact must stay one line per session")
	}
}

func TestColourModes(t *testing.T) {
	frame := func(mode Mode) string {
		o := Options{Mode: mode}
		m := newModel(&o)
		m.v, m.have = fixtureView(), true
		return strings.Join(m.frame(100, 0, true), "\n")
	}
	if s := frame(ModePlain); strings.Contains(s, "\x1b") {
		t.Errorf("plain frame has escapes: %q", s)
	}
	mono := frame(ModeMono)
	if regexp.MustCompile("\x1b\\[[0-9;]*(3[0-9]|9[0-9]|38|48)[;m]").MatchString(mono) {
		t.Errorf("NO_COLOR frame has colour codes")
	}
	if !strings.Contains(mono, "\x1b[1m") {
		t.Errorf("NO_COLOR frame lost bold")
	}
	if s := frame(ModeTrue); !strings.Contains(s, "38;2;245;158;11") {
		t.Errorf("true colour frame lacks the work account colour")
	}
	s := frame(Mode256)
	if strings.Contains(s, "38;2;") || !strings.Contains(s, "38;5;") {
		t.Errorf("256 frame: %q", s)
	}
	for _, c := range []struct{ r, g, b, want int }{{0, 0, 0, 16}, {255, 255, 255, 231}, {255, 0, 0, 196}, {128, 128, 128, 244}} {
		if got := nearest256(c.r, c.g, c.b); got != c.want {
			t.Errorf("nearest256(%d,%d,%d) = %d, want %d", c.r, c.g, c.b, got, c.want)
		}
	}
}

func TestDetect(t *testing.T) {
	env := func(kv ...string) termenv.Env {
		vars := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			vars[kv[i]] = kv[i+1]
		}
		return termenv.Fake(t.TempDir(), "linux", vars)
	}
	cases := []struct {
		env  termenv.Env
		tty  bool
		want Mode
	}{
		{env("TERM", "xterm"), false, ModePlain},
		{env("TERM", "dumb"), true, ModePlain},
		{env("TERM", "xterm", "NO_COLOR", "1", "COLORTERM", "truecolor"), true, ModeMono},
		{env("TERM", "xterm", "COLORTERM", "24bit"), true, ModeTrue},
		{env("TERM", "xterm-256color"), true, Mode256},
	}
	for i, c := range cases {
		if got := DetectMode(c.env, c.tty); got != c.want {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
	if !DetectASCII(env("LANG", "C")) || DetectASCII(env("LANG", "en_US.UTF-8")) || DetectASCII(env()) ||
		DetectASCII(env("LC_ALL", "en_US.UTF-8", "LANG", "C")) || !DetectASCII(env("LC_ALL", "POSIX", "LANG", "en_US.UTF-8")) {
		t.Errorf("DetectASCII wrong")
	}
}

func TestASCII(t *testing.T) {
	o := Options{Mode: ModePlain, ASCII: true}
	m := newModel(&o)
	m.v, m.have, m.cursor = fixtureView(), true, idAsked
	for _, cols := range []int{40, 100} {
		s := strings.Join(m.frame(cols, 0, true), "\n")
		for _, r := range s {
			if r > 0x7e {
				t.Fatalf("non-ASCII %q at %d columns:\n%s", r, cols, s)
			}
		}
	}
	s := strings.Join(m.frame(100, 0, false), "\n")
	for _, g := range []string{"! api", "* api - flaky-tests", "x notes", "o blog"} {
		if !strings.Contains(s, g) {
			t.Errorf("ascii glyph %q missing:\n%s", g, s)
		}
	}
}

func TestContextThresholds(t *testing.T) {
	o := Options{Mode: ModeTrue}
	m := newModel(&o)
	m.v, m.have = fixtureView(), true
	s := strings.Join(m.frame(100, 0, false), "\n")
	if !strings.Contains(s, "79%") || !strings.Contains(s, "80%") || !strings.Contains(s, "92%") {
		t.Fatalf("context values missing:\n%s", s)
	}
	for _, w := range []string{"\x1b[2m  79%\x1b[0m", "\x1b[33m  80%\x1b[0m", "\x1b[1;31m  92%\x1b[0m"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q (79 ok-dim, 80 warn-yellow, 92 high-red):\n%q", w, s)
		}
	}
}

// ---- keys over a fake terminal ----

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// scripted yields one key chunk per Read, then end of input.
type scripted struct{ chunks []string }

func (s *scripted) Read(p []byte) (int, error) {
	if len(s.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.chunks[0])
	s.chunks = s.chunks[1:]
	return n, nil
}

type fakeTerm struct {
	out     *syncBuf
	focused []string
	opened  []string
	refresh int32
	mu      sync.Mutex
}

func (f *fakeTerm) run(t *testing.T, v board.View, keys ...string) string {
	t.Helper()
	views := make(chan board.View, 1)
	views <- v
	o := Options{
		In: &scripted{keys}, Out: f.out, Mode: ModePlain, AltScreen: true,
		Size:   func() (int, int) { return 100, 40 },
		Filter: FilterAll,
		Focus: func(_ context.Context, id string) error {
			f.mu.Lock()
			f.focused = append(f.focused, id)
			f.mu.Unlock()
			return nil
		},
		OpenURL: func(_ context.Context, u string) error {
			f.mu.Lock()
			f.opened = append(f.opened, u)
			f.mu.Unlock()
			return nil
		},
		Refresh: func() { atomic.AddInt32(&f.refresh, 1) },
	}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), views, o) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	return f.out.String()
}

const (
	up    = "\x1b[A"
	down  = "\x1b[B"
	enter = "\r"
)

func TestKeysMoveFilterSearch(t *testing.T) {
	f := &fakeTerm{out: &syncBuf{}}
	out := f.run(t, fixtureView(), down, "j", "k", "f")
	if !strings.Contains(out, "needs you") { // the filter label is in the header
		t.Errorf("f did not show the needs-you filter:\n%s", out)
	}
	if !strings.HasPrefix(out, enterScreen) || !strings.HasSuffix(out, leaveScreen) {
		t.Errorf("screen not entered/left: %q ... %q", out[:20], out[len(out)-30:])
	}

	// move
	o := Options{Mode: ModePlain}
	m := newModel(&o)
	m.v, m.have = fixtureView(), true
	m.settle()
	if m.cursor != idAsked {
		t.Fatalf("starts on %s", m.cursor)
	}
	m.press(key{kind: kDown})
	m.press(key{kind: kRune, r: 'j'})
	if m.cursor != idWork {
		t.Errorf("after down, j: %s", m.cursor)
	}
	m.press(key{kind: kRune, r: 'k'})
	if m.cursor != idPerm {
		t.Errorf("after k: %s", m.cursor)
	}
	m.press(key{kind: kEnd})
	if m.cursor != idRem {
		t.Errorf("End: %s", m.cursor)
	}
	m.press(key{kind: kHome})
	if m.cursor != idAsked {
		t.Errorf("Home: %s", m.cursor)
	}

	// filter cycle: all -> needs -> here -> machine:gpu-box -> all
	var seen []string
	for i := 0; i < 4; i++ {
		m.press(key{kind: kRune, r: 'f'})
		seen = append(seen, m.filter)
	}
	if strings.Join(seen, ",") != "needs,here,machine:gpu-box,all" {
		t.Errorf("filter cycle %v", seen)
	}
	m.press(key{kind: kRune, r: 'f'}) // needs
	if ids := m.order(); len(ids) != 2 || ids[0] != idAsked || ids[1] != idPerm {
		t.Errorf("needs filter shows %v", ids)
	}
	m.filter = "machine:gpu-box"
	m.settle()
	if ids := m.order(); len(ids) != 1 || ids[0] != idRem {
		t.Errorf("machine filter shows %v", ids)
	}
	m.filter = FilterAll

	// search: project, title, account; Esc clears
	m.press(key{kind: kRune, r: '/'})
	for _, r := range "FLAKY" {
		m.press(key{kind: kRune, r: r})
	}
	if ids := m.order(); len(ids) != 1 || ids[0] != idWork {
		t.Errorf("search flaky shows %v", ids)
	}
	m.press(key{kind: kEnter})
	if m.typing || m.search != "FLAKY" {
		t.Errorf("Enter should keep the search: typing=%v search=%q", m.typing, m.search)
	}
	m.press(key{kind: kEsc})
	if m.search != "" || len(m.order()) != 6 {
		t.Errorf("Esc should clear: %q %v", m.search, m.order())
	}
	m.press(key{kind: kRune, r: '/'})
	for _, r := range "personal" {
		m.press(key{kind: kRune, r: r})
	}
	if len(m.order()) != 2 {
		t.Errorf("account search: %v", m.order())
	}
	m.press(key{kind: kBackspace})
	m.press(key{kind: kEsc})
	if m.typing || m.search != "" {
		t.Errorf("Esc in the box must clear and close it")
	}

	// e shows ended rows
	m.press(key{kind: kRune, r: 'e'})
	if len(m.order()) != 7 {
		t.Errorf("e: %d rows", len(m.order()))
	}
	// q quits, Ctrl-C quits, even while typing
	if !m.press(key{kind: kRune, r: 'q'}).quit {
		t.Errorf("q must quit")
	}
	m.press(key{kind: kRune, r: '/'})
	if !m.press(key{kind: kCtrlC}).quit {
		t.Errorf("Ctrl-C must quit")
	}
}

func TestEnterWithoutFocus(t *testing.T) {
	f := &fakeTerm{out: &syncBuf{}}
	// down to the permission row, which has no focus link, then Enter
	out := f.run(t, fixtureView(), down, enter)
	if len(f.focused) != 0 || len(f.opened) != 0 {
		t.Errorf("opener ran: focus %v open %v", f.focused, f.opened)
	}
	if !strings.Contains(out, "no tab to focus") {
		t.Errorf("no message:\n%s", out)
	}
}

func TestEnterFocusAndOpen(t *testing.T) {
	f := &fakeTerm{out: &syncBuf{}}
	f.run(t, fixtureView(), enter)
	if len(f.focused) != 1 || f.focused[0] != idAsked {
		t.Errorf("focus calls %v", f.focused)
	}
	// o on a row without a claude.ai link says so; with one it opens it
	f = &fakeTerm{out: &syncBuf{}}
	out := f.run(t, fixtureView(), "o")
	if len(f.opened) != 0 || !strings.Contains(out, "no claude.ai link") {
		t.Errorf("o without link: %v\n%s", f.opened, out)
	}
	f = &fakeTerm{out: &syncBuf{}}
	f.run(t, fixtureView(), "G", "o")
	if len(f.opened) != 1 || f.opened[0] != "https://claude.ai/code/session_abc" {
		t.Errorf("o opened %v", f.opened)
	}
	// a link that is not https://claude.ai/ is never opened
	v := fixtureView()
	r := v.Rows[idRem]
	r.RemoteURL = "https://claude.ai.evil.example/x"
	v.Rows[idRem] = r
	f = &fakeTerm{out: &syncBuf{}}
	f.run(t, v, "G", "o")
	if len(f.opened) != 0 {
		t.Errorf("opened %v", f.opened)
	}
}

func TestFocusErrorShown(t *testing.T) {
	views := make(chan board.View, 1)
	views <- fixtureView()
	out := &syncBuf{}
	gotErr := make(chan struct{})
	o := Options{In: &blockingKeys{chunks: []string{enter}, release: gotErr}, Out: out, Size: func() (int, int) { return 100, 40 },
		Focus: func(context.Context, string) error { return errors.New("no usable focus URL here") }}
	go func() {
		for !strings.Contains(out.String(), "no usable focus URL here") {
			time.Sleep(5 * time.Millisecond)
		}
		close(gotErr)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Run(ctx, views, o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "focus: no usable focus URL here") {
		t.Errorf("error not shown:\n%s", out.String())
	}
}

// blockingKeys yields its chunks, then blocks until release closes, then EOF.
type blockingKeys struct {
	chunks  []string
	release chan struct{}
}

func (b *blockingKeys) Read(p []byte) (int, error) {
	if len(b.chunks) > 0 {
		n := copy(p, b.chunks[0])
		b.chunks = b.chunks[1:]
		return n, nil
	}
	<-b.release
	return 0, io.EOF
}

func TestCopyOSC52Exact(t *testing.T) {
	f := &fakeTerm{out: &syncBuf{}}
	out := f.run(t, fixtureView(), "c")
	exact := Osc52("claude-2 --resume " + idAsked)
	if !strings.Contains(out, exact) {
		t.Fatalf("OSC 52 bytes missing:\n%q", out)
	}
	if exact != "\x1b]52;c;Y2xhdWRlLTIgLS1yZXN1bWUgYzQxZTJhOTAtNWI3ZC00YzFlLTlmMGEtMmQzYjRjNWU2Zjcw\a" {
		t.Errorf("OSC 52 is %q", exact)
	}
	if !strings.Contains(out, "copied: claude-2 --resume "+idAsked) {
		t.Errorf("footer echo missing:\n%s", out)
	}
	// a row with no resume command copies nothing
	f = &fakeTerm{out: &syncBuf{}}
	out = f.run(t, fixtureView(), down, "c")
	if strings.Contains(out, "\x1b]52;") || !strings.Contains(out, "nothing to copy") {
		t.Errorf("copy of an empty resume:\n%q", out)
	}
}

func TestRefreshKey(t *testing.T) {
	f := &fakeTerm{out: &syncBuf{}}
	f.run(t, fixtureView(), "r")
	if atomic.LoadInt32(&f.refresh) != 1 {
		t.Errorf("refresh calls %d", f.refresh)
	}
}

func TestHelp(t *testing.T) {
	f := &fakeTerm{out: &syncBuf{}}
	out := f.run(t, fixtureView(), "?")
	if !strings.Contains(out, "never types into a session") {
		t.Errorf("no help:\n%s", out)
	}
}

func TestParseKeys(t *testing.T) {
	got := parseKeys([]byte("\x1b[A\x1b[B\x1bOA\x1b[5~j\x1b[<0;1;1M\x03\r\x7fé\x1b"))
	kinds := []keyKind{kUp, kDown, kUp, kPgUp, kRune, kCtrlC, kEnter, kBackspace, kRune, kEsc}
	if len(got) != len(kinds) {
		t.Fatalf("%d keys: %+v", len(got), got)
	}
	for i, k := range kinds {
		if got[i].kind != k {
			t.Errorf("key %d is %v, want %v", i, got[i].kind, k)
		}
	}
	if got[8].r != 'é' {
		t.Errorf("rune %q", got[8].r)
	}
}

func TestEndOfInputQuitsAndNoView(t *testing.T) {
	// views closed before any View: Run returns nil
	views := make(chan board.View)
	close(views)
	if err := Run(context.Background(), views, Options{Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	// context cancel
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, make(chan board.View), Options{Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
}

func TestResizeRedraws(t *testing.T) {
	views := make(chan board.View, 1)
	views <- fixtureView()
	out := &syncBuf{}
	var cols atomic.Int32
	cols.Store(100)
	resize := make(chan struct{}, 1)
	rel := make(chan struct{})
	o := Options{In: &blockingKeys{release: rel}, Out: out, Resize: resize, Size: func() (int, int) { return int(cols.Load()), 30 }}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), views, o) }()
	waitFor(t, func() bool { return strings.Contains(out.String(), "Update the migration") })
	cols.Store(40)
	resize <- struct{}{}
	waitFor(t, func() bool {
		parts := strings.Split(out.String(), "\x1b[2J")
		return len(parts) >= 3 && strings.Contains(parts[len(parts)-1], "↑↓ ⏎ c o f / e r ? q")
	})
	close(rel)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestScrollKeepsCursorVisible(t *testing.T) {
	o := Options{Mode: ModePlain}
	m := newModel(&o)
	m.v, m.have = bigView(50), true
	m.settle()
	for i := 0; i < 30; i++ {
		m.press(key{kind: kDown})
		lines := m.frame(80, 20, true)
		if len(lines) != 20 {
			t.Fatalf("frame has %d lines, want 20", len(lines))
		}
		if !strings.Contains(strings.Join(lines, "\n"), "›● proj"+strconv.Itoa(i+1)+" ") {
			t.Fatalf("cursor row %d not on screen:\n%s", i+1, strings.Join(lines, "\n"))
		}
	}
	m.press(key{kind: kHome})
	lines := m.frame(80, 20, true)
	if !strings.Contains(lines[1], "work") {
		t.Errorf("Home should show the account header: %q", lines[1])
	}
}

func TestHubStates(t *testing.T) {
	v := fixtureView()
	since := time.Date(2026, 10, 20, 14, 2, 0, 0, time.UTC)
	v.Hub = board.HubInfo{State: board.HubOffline, OfflineSince: &since}
	o := Options{Mode: ModePlain}
	s := RenderOnce(v, 100, o)
	if !strings.Contains(s, "hub offline since "+since.Local().Format("15:04")) {
		t.Errorf("offline header:\n%s", s)
	}
	v.Hub = board.HubInfo{State: board.HubUnpaired}
	s = RenderOnce(v, 100, o)
	if !strings.Contains(s, "run `sy board connect`") {
		t.Errorf("unpaired hint:\n%s", s)
	}
	v.Hub = board.HubInfo{State: board.HubNone}
	if s = RenderOnce(v, 100, o); !strings.Contains(s, "3 machines") && !strings.Contains(s, "2 machines · local") {
		t.Errorf("none header:\n%s", s)
	}
	// greyed rows are dim
	v.Hub = board.HubInfo{State: board.HubLive}
	r := v.Rows[idRem]
	r.Greyed = true
	v.Rows[idRem] = r
	s = RenderOnce(v, 100, Options{Mode: ModeMono})
	if !strings.Contains(s, "\x1b[2m") {
		t.Errorf("greyed row not dim")
	}
}

// ---- budgets and write discipline ----

func TestRedraw300RowsUnder16ms(t *testing.T) {
	o := Options{Mode: ModeTrue}
	m := newModel(&o)
	m.v, m.have = bigView(300), true
	m.settle()
	scr := &screen{o: &Options{Out: io.Discard, Size: func() (int, int) { return 120, 50 }}}
	best := time.Hour
	for i := 0; i < 30; i++ {
		m.press(key{kind: kDown})
		start := time.Now()
		scr.prev = nil // worst case: every line changes
		scr.draw(m, "")
		if d := time.Since(start); d < best {
			best = d
		}
	}
	if best > raceSlowdown*16*time.Millisecond {
		t.Errorf("redraw of 300 rows took %v", best)
	}
}

// The TUI package writes to Options.Out and nothing else: no os.Stdout/Stderr, no files, no
// /dev/tty in its non-test sources.
func TestWritesOnlyToItsOutput(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	bad := regexp.MustCompile(`os\.(Stdout|Stderr|OpenFile|Create|WriteFile|Open)\b|/dev/tty|CONOUT|fmt\.Print`)
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(n)
		if loc := bad.FindIndex(b); loc != nil {
			t.Errorf("%s uses %q", n, b[loc[0]:loc[1]])
		}
	}
	// and at run time every byte goes through Out
	f := &fakeTerm{out: &syncBuf{}}
	if out := f.run(t, fixtureView(), "c", "?", "x"); out == "" {
		t.Errorf("nothing written to Out")
	}
}
