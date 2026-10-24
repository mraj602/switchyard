// Package terminal is the end-to-end suite for the terminal features (scenarios E1..E11;
// docs/spec/terminal.md T2, T3, T6, T7, T10, T11): the real `wy` binary,
// built once with `-tags fakeopen`, against a temp home laid out like the laptop of
// setup.md 11 (detecttest.Laptop), a real `wy hub serve` on a free loopback port, a second
// machine ("gpu-box", its own HOME and WY_HOME), the fakeclaude of the setup suite on PATH
// and a file-recording opener (WY_TEST_OPENER).
//
//	make e2e-terminal       (go test -count=1 -v -timeout 15m ./test/e2e/terminal)
//
// Every subprocess gets an environment built from scratch (envFor): a temp HOME and WY_HOME,
// PATH = temp bin dirs + /usr/bin:/bin, and nothing from the parent. Nothing here reads or
// writes the real ~/.claude*, Warp's folders, ~/.config/watchyard or any service. Skipped
// with -short and on Windows.
package terminal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
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

	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/watchyard/internal/setup/testtree"
)

// publicURL is the address the hub believes its browsers open (owner sessions check the
// Origin against it); it is never dialled.
const publicURL = "https://hub.e2e.test"

// ---- build once ----

var (
	buildOnce sync.Once
	buildErr  error
	buildRoot string // temp folder holding the binaries
	syBin     string // wy built with -tags fakeopen
	fakeDir   string // folder holding fakeclaude as "claude"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if buildRoot != "" {
		_ = os.RemoveAll(buildRoot)
	}
	os.Exit(code)
}

// goEnv is the environment of the `go build` runs only (wy is never run with it): the
// parent's, without anything that points at a real Claude or wy folder.
func goEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "WY_") || strings.HasPrefix(k, "XDG_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// need skips the test where the suite cannot run and builds the binaries on first use.
func need(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end: builds and runs wy")
	}
	if runtime.GOOS == "windows" {
		t.Skip("end-to-end: unix only")
	}
	buildOnce.Do(func() {
		out, err := exec.Command("go", "env", "GOMOD").Output()
		if err != nil {
			buildErr = fmt.Errorf("go env GOMOD: %w", err)
			return
		}
		root := filepath.Dir(strings.TrimSpace(string(out)))
		buildRoot, err = os.MkdirTemp("", "sy-e2e-term-")
		if err != nil {
			buildErr = err
			return
		}
		build := func(out string, args ...string) error {
			cmd := exec.Command("go", append([]string{"build", "-o", out}, args...)...)
			cmd.Dir = root
			cmd.Env = goEnv()
			if b, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("go build %v: %v\n%s", args, err, b)
			}
			return nil
		}
		syBin = filepath.Join(buildRoot, "wy")
		fakeDir = filepath.Join(buildRoot, "fake")
		if buildErr = build(syBin, "-tags", "fakeopen", "./cmd/wy"); buildErr != nil {
			return
		}
		buildErr = build(filepath.Join(fakeDir, "claude"), "./test/e2e/setup/fakeclaude")
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
}

// ---- running wy ----

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

// machine is one `wy` identity: a HOME, an WY_HOME and the environment built from them.
type machine struct {
	t      *testing.T
	name   string
	home   string
	syHome string
	id     string // machine id, once known
	// pathDirs come first on PATH, before /usr/bin:/bin.
	pathDirs []string
	extra    []string // more KEY=VALUE for every run
}

// envFor builds a subprocess environment from scratch.
func (m *machine) envFor(extra ...string) []string {
	env := []string{
		"PATH=" + strings.Join(append(append([]string{}, m.pathDirs...), "/usr/bin", "/bin"), ":"),
		"HOME=" + m.home,
		"WY_HOME=" + m.syHome,
		"WY_NO_SPAWN=1",
		"SHELL=/bin/sh",
		"TERM=dumb",
		"TZ=UTC",
		"NO_COLOR=1",
	}
	env = append(env, m.extra...)
	return append(env, extra...)
}

// merge returns a copy of env where later KEY=VALUE entries replace earlier ones; a bare
// "KEY=" with nothing after it removes the key.
func merge(env []string, extra ...string) []string {
	out := append([]string{}, env...)
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		kept := out[:0:0]
		for _, e := range out {
			if !strings.HasPrefix(e, k+"=") {
				kept = append(kept, e)
			}
		}
		out = kept
		if v != "" {
			out = append(out, kv)
		}
	}
	return out
}

type result struct {
	out, err string
	code     int
}

// run runs wy with stdin and extra environment.
func (m *machine) run(stdin string, extra []string, args ...string) result {
	m.t.Helper()
	cmd := exec.Command(syBin, args...)
	cmd.Env = m.envFor(extra...)
	cmd.Dir = m.home
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	r := result{}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			m.t.Fatalf("wy %v: %v", args, err)
		}
		r.code = ee.ExitCode()
	}
	r.out, r.err = o.String(), e.String()
	return r
}

// wy runs wy and returns the result, whatever its exit code.
func (m *machine) wy(args ...string) result { m.t.Helper(); return m.run("", nil, args...) }

// must runs wy and fails unless it exits 0; it returns stdout.
func (m *machine) must(args ...string) string {
	m.t.Helper()
	r := m.run("", nil, args...)
	if r.code != 0 {
		m.t.Fatalf("[%s] wy %s: exit %d\nstdout:\n%s\nstderr:\n%s", m.name, strings.Join(args, " "), r.code, r.out, r.err)
	}
	return r.out
}

// mustJSON runs wy and decodes its stdout into v.
func (m *machine) mustJSON(v any, args ...string) {
	m.t.Helper()
	out := m.must(args...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		m.t.Fatalf("[%s] wy %s: %v\n%s", m.name, strings.Join(args, " "), err, out)
	}
}

// ---- machines ----

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// laptop is the temp home of detecttest.Laptop with fakeclaude as its claude, a wrapper
// `claude` that logs every argv in front of it (and points Warp's GitHub marketplace at a
// local copy), and the opener's record file.
type laptop struct {
	*machine
	h       *detecttest.Home
	wrapLog string // argv log of the wrapper claude: "<CLAUDE_CONFIG_DIR or <unset>>|<args>"
	openLog string // WY_TEST_OPENER file
}

func newLaptop(t *testing.T) *laptop {
	t.Helper()
	need(t)
	h := detecttest.Laptop(t)
	// The fixture's ~/.local/bin/claude is an empty stub: make it fakeclaude.
	writeFile(t, h.Abs(".local/bin/claude"), readFile(filepath.Join(fakeDir, "claude")), 0o755)

	syHome := t.TempDir()
	l := &laptop{h: h, wrapLog: filepath.Join(syHome, "wrap.log"), openLog: filepath.Join(syHome, "open.log")}
	l.machine = &machine{t: t, name: "laptop", home: h.Root, syHome: syHome}
	src := filepath.Join(syHome, "warp-src")
	writeFile(t, filepath.Join(src, ".claude-plugin", "marketplace.json"),
		`{"name":"claude-code-warp","owner":{"name":"warp"},"plugins":[{"name":"warp","source":"./plugins/warp"}]}`, 0o644)
	writeFile(t, filepath.Join(src, "plugins", "warp", ".claude-plugin", "plugin.json"), `{"name":"warp","version":"1.0.0"}`, 0o644)
	fake := filepath.Join(fakeDir, "claude")
	writeFile(t, h.Abs(".local/bin/claude"), `#!/bin/sh
printf '%s|%s\n' "${CLAUDE_CONFIG_DIR-<unset>}" "$*" >> "`+l.wrapLog+`"
if [ "$1 $2 $3 $4" = "plugin marketplace add warpdotdev/claude-code-warp#main" ]; then
  shift 4
  exec "`+fake+`" plugin marketplace add "`+src+`" "$@"
fi
exec "`+fake+`" "$@"
`, 0o755)
	l.pathDirs = []string{h.Abs(".local/bin"), fakeDir}
	l.extra = []string{"FAKE_CLAUDE_LOG=" + filepath.Join(syHome, "claude.log"), "WY_TEST_OPENER=" + l.openLog}
	return l
}

// setup runs `wy setup --yes --hub later --named-launchers`, as the term tests do.
func (l *laptop) setup() {
	l.t.Helper()
	l.must("setup", "--yes", "--hub", "later", "--named-launchers")
}

// wrapRuns is the logged `claude plugin …` runs (not `plugin list`).
func (l *laptop) wrapRuns() []string {
	var out []string
	for _, ln := range strings.Split(strings.TrimSpace(readFile(l.wrapLog)), "\n") {
		if strings.Contains(ln, "|plugin ") && !strings.Contains(ln, " list") {
			out = append(out, ln)
		}
	}
	return out
}

// opened is the opener's record: one argv per line.
func (m *machine) opened(file string) [][]string {
	var out [][]string
	for _, ln := range strings.Split(strings.TrimSpace(readFile(file)), "\n") {
		if ln == "" {
			continue
		}
		var argv []string
		if err := json.Unmarshal([]byte(ln), &argv); err != nil {
			m.t.Fatalf("opener record %q: %v", ln, err)
		}
		out = append(out, argv)
	}
	return out
}

// snapOpts ignores what fakeclaude rewrites on every run.
var snapOpts = testtree.Opts{Ignore: func(rel string) bool {
	return filepath.Base(rel) == ".claude.json"
}}

// newBox is a second machine with its own plain HOME and WY_HOME (the "gpu-box").
func newBox(t *testing.T, name string) *machine {
	t.Helper()
	need(t)
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), "{}\n", 0o644)
	writeFile(t, filepath.Join(home, ".claude", ".claude.json"), `{"oauthAccount":{"emailAddress":"box@example.com"}}`, 0o600)
	m := &machine{t: t, name: name, home: home, syHome: t.TempDir(), pathDirs: []string{fakeDir}}
	m.extra = []string{"WY_TEST_OPENER=" + filepath.Join(m.syHome, "open.log")}
	return m
}

// stateDir is <WY_HOME>/state.
func (m *machine) stateDir() string { return filepath.Join(m.syHome, "state") }

// machineID asks `wy desktop info` for this machine's id.
func (m *machine) machineID() string {
	m.t.Helper()
	if m.id == "" {
		var info struct {
			Machine struct{ ID string } `json:"machine"`
		}
		m.mustJSON(&info, "desktop", "info", "--json")
		if info.Machine.ID == "" {
			m.t.Fatal("no machine id")
		}
		m.id = info.Machine.ID
	}
	return m.id
}

// ---- the hub ----

type hubProc struct {
	t      *testing.T
	m      *machine // whose WY_HOME the hub runs in
	addr   string
	base   string
	cmd    *exec.Cmd
	log    *syncBuf
	done   chan struct{}
	cookie string // owner session cookie, once made
}

// freeAddr finds a free loopback port (and releases it, so a restart can reuse it).
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// startHub runs `wy hub serve` in m's WY_HOME on a free loopback port and registers its
// stop for the end of the test.
func startHub(t *testing.T, m *machine) *hubProc {
	t.Helper()
	h := &hubProc{t: t, m: m, addr: freeAddr(t)}
	h.base = "http://" + h.addr
	t.Cleanup(h.kill)
	h.start()
	return h
}

func (h *hubProc) start() {
	h.t.Helper()
	cmd := exec.Command(syBin, "hub", "serve", "--listen", h.addr, "--public-url", publicURL, "--no-push", "--no-pull")
	cmd.Env = h.m.envFor()
	cmd.Dir = h.m.home
	h.log = &syncBuf{}
	pr, pw := io.Pipe()
	cmd.Stderr, cmd.Stdout = pw, h.log
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	h.cmd = cmd
	done := make(chan struct{})
	h.done = done
	go func() { _ = cmd.Wait(); pw.Close(); close(done) }()
	up := make(chan struct{}, 1)
	listening := regexp.MustCompile(`listening on http://`)
	log := h.log
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			fmt.Fprintln(log, sc.Text())
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
		h.t.Fatalf("hub exited:\n%s", log)
	case <-time.After(30 * time.Second):
		h.t.Fatalf("hub did not start:\n%s", log)
	}
}

// kill is a crash: SIGKILL, no graceful close.
func (h *hubProc) kill() {
	if h.cmd == nil || h.cmd.Process == nil {
		return
	}
	_ = h.cmd.Process.Kill()
	<-h.done
	h.cmd = nil
}

// restart starts the hub again on the same address and database.
func (h *hubProc) restart() { h.t.Helper(); h.kill(); h.start() }

// get reads one API path with a bearer token and decodes it into v.
func (h *hubProc) get(token, path string, v any) {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.base+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		h.t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, v); err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
}

// connect makes m a machine of the hub: hub.url, a machine token in <state>/token (what
// `wy flush` pushes with). The hub's own WY_HOME is registered through the same command.
func (h *hubProc) connect(m *machine) {
	h.t.Helper()
	id := m.machineID()
	m.must("config", "set", "hub.url", h.base)
	var tok struct{ Token string }
	h.m.mustJSON(&tok, "hub", "token", "create", "--kind", "machine", "--name", m.name, "--machine", id, "--json")
	if tok.Token == "" {
		h.t.Fatal("no machine token")
	}
	writeFile(h.t, filepath.Join(m.stateDir(), "token"), tok.Token+"\n", 0o600)
}

// ownerToken is a desktop-kind token the test reads the hub with (the web board's data
// endpoints take the same bearer; the hub serves /v1/sessions and /v1/accounts to it).
func (h *hubProc) readToken(bind *machine) string {
	h.t.Helper()
	var tok struct{ Token string }
	h.m.mustJSON(&tok, "hub", "token", "create", "--kind", "desktop", "--name", "e2e-reader", "--machine", bind.machineID(), "--json")
	if tok.Token == "" {
		h.t.Fatal("no reader token")
	}
	return tok.Token
}

// ---- records ----

type seatDef struct {
	Account, Org, Email string
}

func (s seatDef) rec() *record.Seat {
	return &record.Seat{Account: s.Account, Org: s.Org, Email: s.Email}
}

var (
	seatWork     = seatDef{"acc-work", "org-work", "work@example.com"}
	seatPersonal = seatDef{"acc-personal", "org-personal", "me@example.com"}
	seatTeam     = seatDef{"acc-team", "org-team", "team@example.com"}
)

// hookRecord is one hook_event record as `wy hook` would write it, with an explicit seat.
func hookRecord(t *testing.T, machineID, slot string, seat seatDef, term *record.Term, sid, event string, fields map[string]any) *record.Record {
	t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + sid + ".jsonl"}
	for k, v := range fields {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	rec, err := record.NewHookEvent(record.Meta{Machine: machineID, Slot: slot, Seat: seat.rec(), Term: term, Client: "wy/e2e", Now: time.Now()},
		raw, record.HookOptions{LastMessages: true})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// viaOutbox is how a machine's records travel: wy outbox append, then wy flush.
func (m *machine) viaOutbox(recs ...*record.Record) {
	m.t.Helper()
	var in bytes.Buffer
	for _, rec := range recs {
		b, err := rec.Encode()
		if err != nil {
			m.t.Fatal(err)
		}
		in.Write(b)
		in.WriteByte('\n')
	}
	if r := m.run(in.String(), nil, "outbox", "append"); r.code != 0 {
		m.t.Fatalf("outbox append: %s%s", r.out, r.err)
	}
	m.must("flush")
}

// hook runs the real `wy hook <event>` with a payload and extra environment, then flushes.
func (m *machine) hook(extra []string, sid, event string, fields map[string]any) {
	m.t.Helper()
	m.hookNoFlush(extra, sid, event, fields)
	m.flush()
}

func (m *machine) hookNoFlush(extra []string, sid, event string, fields map[string]any) {
	m.t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/home/u/api",
		"transcript_path": "/home/u/.claude/projects/x/" + sid + ".jsonl"}
	for k, v := range fields {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	if r := m.run(string(raw), extra, "hook", event); r.code != 0 {
		m.t.Fatalf("[%s] wy hook %s: exit %d %s%s", m.name, event, r.code, r.out, r.err)
	}
}

// flush pushes the outbox to the hub; it never fails the test (the hub may be down).
func (m *machine) flush() { m.run("", nil, "flush") }

// ---- board ----

// boardView runs `wy board --once --json` and decodes the View.
func (m *machine) boardView(extra ...string) boardView {
	m.t.Helper()
	var v boardView
	m.mustJSON(&v, append([]string{"board", "--once", "--json"}, extra...)...)
	return v
}

// boardView is the part of the View (terminal.md T2) the suite reads.
type boardView struct {
	V      int    `json:"v"`
	Source string `json:"source"`
	Hub    struct {
		State        string  `json:"state"`
		OfflineSince *string `json:"offline_since"`
	} `json:"hub"`
	Counts struct {
		Sessions int `json:"sessions"`
		Machines int `json:"machines"`
		NeedsYou int `json:"needs_you"`
	} `json:"counts"`
	NeedsYou []string `json:"needs_you"`
	Groups   []struct {
		Seat  string   `json:"seat"`
		Label string   `json:"label"`
		Rows  []string `json:"rows"`
	} `json:"groups"`
	Rows map[string]boardRow `json:"rows"`
}

type boardRow struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Detail      string `json:"detail"`
	NeedsYou    bool   `json:"needs_you"`
	Status      string `json:"status"`
	Account     string `json:"account"`
	Machine     string `json:"machine"`
	Here        bool   `json:"here"`
	ThisSession bool   `json:"this_session"`
	Line2       string `json:"line2"`
	Kind        string `json:"line2_kind"`
	Greyed      bool   `json:"greyed"`
	CanFocus    bool   `json:"can_focus"`
}

// order is the row ids in display order: the groups in order, their rows in order.
func (v boardView) order() []string {
	var ids []string
	for _, g := range v.Groups {
		ids = append(ids, g.Rows...)
	}
	return ids
}

// connectBoard makes the board token of m (the hub's host) with the local admin channel.
func (h *hubProc) connectBoard(m *machine) {
	h.t.Helper()
	m.must("board", "connect")
}

// follower is `wy board --follow --ndjson` running in the background.
type follower struct {
	t     *testing.T
	cmd   *exec.Cmd
	mu    sync.Mutex
	views []boardView
	raw   []time.Time // when each line arrived
	errb  *syncBuf
	done  chan struct{}
}

func (m *machine) follow(extra ...string) *follower {
	m.t.Helper()
	f := &follower{t: m.t, errb: &syncBuf{}, done: make(chan struct{})}
	cmd := exec.Command(syBin, append([]string{"board", "--follow", "--ndjson"}, extra...)...)
	cmd.Env = m.envFor()
	cmd.Dir = m.home
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, f.errb
	// stdin is /dev/null: the board reads nothing from here.
	if err := cmd.Start(); err != nil {
		m.t.Fatal(err)
	}
	f.cmd = cmd
	go func() { _ = cmd.Wait(); pw.Close() }()
	go func() {
		defer close(f.done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			var v boardView
			if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
				continue
			}
			f.mu.Lock()
			f.views = append(f.views, v)
			f.raw = append(f.raw, time.Now())
			f.mu.Unlock()
		}
	}()
	m.t.Cleanup(f.stop)
	return f
}

func (f *follower) stop() {
	if f.cmd != nil && f.cmd.Process != nil {
		_ = f.cmd.Process.Signal(os.Interrupt)
		select {
		case <-f.done:
		case <-time.After(5 * time.Second):
			_ = f.cmd.Process.Kill()
		}
		f.cmd = nil
	}
}

// last is the newest line.
func (f *follower) last() boardView {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := len(f.views); n > 0 {
		return f.views[n-1]
	}
	return boardView{}
}

// count is how many lines have arrived.
func (f *follower) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.views)
}

// wait waits for a line (after the first `from` lines) that satisfies ok.
func (f *follower) wait(from int, d time.Duration, what string, ok func(boardView) bool) boardView {
	f.t.Helper()
	v, found := f.tryWait(from, d, ok)
	if !found {
		f.mu.Lock()
		var last boardView
		if n := len(f.views); n > 0 {
			last = f.views[n-1]
		}
		f.mu.Unlock()
		f.t.Fatalf("no line within %s where %s (%d lines; stderr %q); last hub=%s counts=%+v rows=%d", d, what, f.count(), f.errb.String(), last.Hub.State, last.Counts, len(last.Rows))
	}
	return v
}

func (f *follower) tryWait(from int, d time.Duration, ok func(boardView) bool) (boardView, bool) {
	deadline := time.Now().Add(d)
	for i := from; ; {
		f.mu.Lock()
		n := len(f.views)
		var v boardView
		if i < n {
			v = f.views[i]
		}
		f.mu.Unlock()
		if i < n {
			if ok(v) {
				return v, true
			}
			i++
			continue
		}
		if time.Now().After(deadline) {
			return boardView{}, false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
