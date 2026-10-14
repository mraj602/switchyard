package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/outbox"
	"github.com/mraj602/switchyard/internal/record"
	"github.com/mraj602/switchyard/internal/termctl"
	"github.com/mraj602/switchyard/internal/termenv"
)

const (
	focusURL1   = "warp://session/0123456789abcdef0123456789abcdef"
	focusSess   = "c41e2a90-5b7d-4c1e-9f0a-2d3b4c5e6f70"
	otherMachID = "m_01HZX3K9Q2M8R5T7V9WBCD4EFG"
)

type fakeFocusOpener struct {
	urls []string
	err  error
}

func (f *fakeFocusOpener) Open(_ context.Context, u string) error {
	f.urls = append(f.urls, u)
	return f.err
}

// focusBox is a temp machine for `sy term focus`: the environment, a fake opener and this
// machine's id.
type focusBox struct {
	t       *testing.T
	home    string
	vars    map[string]string
	opener  *fakeFocusOpener
	machine string
}

func newFocusBox(t *testing.T, vars map[string]string) *focusBox {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SY_HOME", filepath.Join(home, "sy"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	fb := &focusBox{t: t, home: home, vars: map[string]string{"SY_HOME": filepath.Join(home, "sy")}, opener: &fakeFocusOpener{}}
	for k, v := range vars {
		fb.vars[k] = v
	}
	env := termenv.Fake(home, runtime.GOOS, fb.vars)
	oldEnv, oldOpen, oldHub := focusEnv, focusOpener, focusHubGet
	focusEnv = func() termenv.Env { return env }
	focusOpener = func(termenv.Env, io.Writer) termctl.Opener { return fb.opener }
	t.Cleanup(func() { focusEnv, focusOpener, focusHubGet = oldEnv, oldOpen, oldHub })
	fb.machine = thisMachineID(env)
	if fb.machine == "" {
		t.Fatal("no machine id")
	}
	return fb
}

func (fb *focusBox) run(args ...string) (int, string) {
	var out, errb bytes.Buffer
	code := dispatch(append([]string{"term", "focus"}, args...), &out, &errb)
	return code, errb.String()
}

func (fb *focusBox) record(machine, host, session, url string) {
	fb.t.Helper()
	term := &record.Term{App: "warp", FocusURL: url, Host: host}
	rec, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: "~/.claude", Term: term, Client: "sy/test"},
		[]byte(`{"hook_event_name":"UserPromptSubmit","session_id":"`+session+`","cwd":"/x/api"}`), record.HookOptions{})
	if err != nil {
		fb.t.Fatal(err)
	}
	if _, err := outbox.New(filepath.Join(fb.home, "sy", "state", "outbox")).Append(rec); err != nil {
		fb.t.Fatal(err)
	}
}

func TestFocusSessionLocal(t *testing.T) {
	fb := newFocusBox(t, nil)
	fb.record(fb.machine, "", focusSess, focusURL1)
	if code, msg := fb.run("--session", focusSess); code != 0 || len(fb.opener.urls) != 1 || fb.opener.urls[0] != focusURL1 {
		t.Fatalf("exit %d %q, opened %v", code, msg, fb.opener.urls)
	}
}

func TestFocusSessionOfAnotherMachineExits4(t *testing.T) {
	fb := newFocusBox(t, nil)
	fb.record(otherMachID, "", focusSess, focusURL1) // recorded by another machine
	if code, msg := fb.run("--session", focusSess); code != 4 || len(fb.opener.urls) != 0 {
		t.Errorf("other machine: exit %d %q, opened %v", code, msg, fb.opener.urls)
	}
	s2 := "9a0b2a90-5b7d-4c1e-9f0a-2d3b4c5e6f70"
	fb.record(fb.machine, otherMachID, s2, focusURL1) // a server session whose tab is on the laptop
	if code, msg := fb.run("--session", s2); code != 4 || len(fb.opener.urls) != 0 {
		t.Errorf("term.host elsewhere: exit %d %q, opened %v", code, msg, fb.opener.urls)
	}
	s3 := "aaaa2a90-5b7d-4c1e-9f0a-2d3b4c5e6f70"
	fb.record(fb.machine, "", s3, "https://evil.example/x") // not on the allow-list
	if code, _ := fb.run("--session", s3); code != 4 || len(fb.opener.urls) != 0 {
		t.Errorf("bad url: exit %d, opened %v", code, fb.opener.urls)
	}
	if code, _ := fb.run("--session", "unknown-session"); code != 4 || len(fb.opener.urls) != 0 {
		t.Errorf("unknown: exit %d", code)
	}
}

func TestFocusSessionFromHub(t *testing.T) {
	fb := newFocusBox(t, nil)
	var auth, path string
	var answer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		_, _ = io.WriteString(w, answer)
	}))
	defer srv.Close()
	tokDir := filepath.Join(fb.home, "sy", "state", "board")
	if err := os.MkdirAll(tokDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tok := `{"v":1,"url":"` + srv.URL + `","device":"d_1","token":"sekret"}`
	if err := os.WriteFile(filepath.Join(tokDir, "token.json"), []byte(tok), 0o600); err != nil {
		t.Fatal(err)
	}
	// The token is used only for the hub of hub.url (T3); without hub.url there is no hub.
	if code, _ := fb.run("--session", focusSess); code != 4 || len(fb.opener.urls) != 0 {
		t.Fatalf("no hub.url: exit %d", code)
	}
	cfgDir := filepath.Join(fb.home, "sy", "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[hub]\nurl = \""+srv.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	answer = `{"id":"` + focusSess + `","focus_url":"` + focusURL1 + `"}`
	if code, msg := fb.run("--session", focusSess); code != 0 || len(fb.opener.urls) != 1 {
		t.Fatalf("exit %d %q", code, msg)
	}
	if auth != "Bearer sekret" || path != "/v1/sessions/"+focusSess {
		t.Errorf("request: %q %q", auth, path)
	}

	fb.opener.urls = nil
	answer = `{"id":"` + focusSess + `","focus_url":null}` // another machine's tab: the hub withholds it
	if code, _ := fb.run("--session", focusSess); code != 4 || len(fb.opener.urls) != 0 {
		t.Errorf("null focus_url: exit %d, opened %v", code, fb.opener.urls)
	}
	answer = `{"focus_url":"https://evil.example/"}`
	if code, _ := fb.run("--session", focusSess); code != 4 || len(fb.opener.urls) != 0 {
		t.Errorf("hub gave a bad url: exit %d, opened %v", code, fb.opener.urls)
	}
}

func TestFocusURL(t *testing.T) {
	fb := newFocusBox(t, map[string]string{"TERM_PROGRAM": "WarpTerminal"})
	if code, msg := fb.run("--url", focusURL1); code != 0 || len(fb.opener.urls) != 1 {
		t.Fatalf("exit %d %q", code, msg)
	}
	fb.opener.urls = nil
	for _, bad := range []string{"file:///etc/passwd", "https://claude.ai/x", "warp://session/ABCDEF0123456789ABCDEF0123456789", focusURL1 + " "} {
		if code, _ := fb.run("--url", bad); code != 4 || len(fb.opener.urls) != 0 {
			t.Errorf("%q: exit %d, opened %v", bad, code, fb.opener.urls)
		}
	}

	out := newFocusBox(t, map[string]string{"TERM_PROGRAM": "xterm"})
	if code, _ := out.run("--url", focusURL1); code != 4 || len(out.opener.urls) != 0 {
		t.Errorf("outside Warp: exit %d, opened %v", code, out.opener.urls)
	}
}

func TestFocusOpenerFailureExits6AndUsage(t *testing.T) {
	fb := newFocusBox(t, map[string]string{"TERM_PROGRAM": "WarpTerminal"})
	fb.opener.err = errors.New("xdg-open: exit 3")
	if code, _ := fb.run("--url", focusURL1); code != 6 {
		t.Errorf("exit %d, want 6", code)
	}
	for _, args := range [][]string{{}, {"--session", "a", "--url", focusURL1}, {"--session", "bad id!"}, {"extra"}} {
		if code, _ := fb.run(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if !strings.Contains(termFocusUsage, "--session") {
		t.Error("usage")
	}
}
