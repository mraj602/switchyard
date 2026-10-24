package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/plan"
	"github.com/mraj602/watchyard/internal/warp"
)

const (
	testHub     = "https://hub.example"
	testToken   = "wy_d_SECRETSECRETSECRET"
	testMachine = "m_01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

// termRun runs checkTerminal on a fake home and returns the lines.
func termRun(t *testing.T, e fakeEnv, o Options, cfg config.Config, ver string) Report {
	t.Helper()
	var rep Report
	checkTerminal(context.Background(), &rep, o, cfg, ver)
	for _, c := range rep.Checks {
		if strings.Contains(c.Detail+c.Fix, testToken) {
			t.Fatalf("token printed in %+v", c)
		}
		if (c.Level == Warn || c.Level == Fail) && (c.Fix == "" || strings.Contains(c.Fix, "\n")) {
			t.Errorf("%s: warn/fail needs a one-line fix: %+v", c.Name, c)
		}
	}
	return rep
}

func lvl(t *testing.T, rep Report, name string) Check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", name, rep.Checks)
	return Check{}
}

func has(rep Report, name string) bool {
	for _, c := range rep.Checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

func termOpts(e fakeEnv) Options {
	o := e.opts(identity.OSFS{})
	o.LookPath = func(n string) (string, error) { return "/fake/bin/" + n, nil }
	return o
}

func setLedger(t *testing.T, e fakeEnv, led model.Ledger) {
	t.Helper()
	if err := ledger.Write(e.dirs.State.Path, led); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalQuietOnEmptyMachine(t *testing.T) {
	e := newFakeHome(t, "{}")
	rep := Run(context.Background(), termOpts(e))
	for _, c := range rep.Checks {
		switch c.Name {
		case "board token", "titles", "title hooks", "title output", "pane", "warp plugin", "warp", "ssh env":
			t.Errorf("unexpected terminal line on an empty machine: %+v", c)
		}
	}
}

func TestBoardToken(t *testing.T) {
	e := newFakeHome(t, "{}")
	o := termOpts(e)
	cfg := config.Defaults()
	cfg.Hub.URL = testHub
	write(t, filepath.Join(e.dirs.State.Path, "machine.json"), `{"id":"`+testMachine+`","name":"x"}`)
	if err := board.SaveToken(e.dirs.State.Path, board.Token{URL: testHub + "/", Device: "terminal-x", Token: testToken}); err != nil {
		t.Fatal(err)
	}
	probe := func(me BoardMe, err error) {
		o.BoardProbe = func(_ context.Context, hub, _ string, tok string) (BoardMe, error) {
			if hub != testHub || tok != testToken {
				t.Errorf("probe got %q %q", hub, tok)
			}
			return me, err
		}
	}

	probe(BoardMe{Kind: "desktop", Machine: testMachine}, nil)
	if c := lvl(t, termRun(t, e, o, cfg, ""), "board token"); c.Level != OK {
		t.Errorf("good token: %+v", c)
	}
	probe(BoardMe{}, errors.New("dial tcp: connection refused"))
	if c := lvl(t, termRun(t, e, o, cfg, ""), "board token"); c.Level != Warn {
		t.Errorf("unreachable hub must warn, not fail: %+v", c)
	}
	probe(BoardMe{}, ErrBoardRejected)
	if c := lvl(t, termRun(t, e, o, cfg, ""), "board token"); c.Level != Warn || !strings.Contains(c.Detail, "rejected") {
		t.Errorf("rejected: %+v", c)
	}
	probe(BoardMe{Kind: "machine", Machine: testMachine}, nil)
	if c := lvl(t, termRun(t, e, o, cfg, ""), "board token"); c.Level != Warn || !strings.Contains(c.Detail, "not a desktop") {
		t.Errorf("wrong kind: %+v", c)
	}
	probe(BoardMe{Kind: "desktop", Machine: "m_other"}, nil)
	if c := lvl(t, termRun(t, e, o, cfg, ""), "board token"); c.Level != Warn || !strings.Contains(c.Detail, "not bound") {
		t.Errorf("wrong machine: %+v", c)
	}

	// Another hub: no probe at all.
	o.BoardProbe = func(context.Context, string, string, string) (BoardMe, error) {
		t.Fatal("probe must not run for a token of another hub")
		return BoardMe{}, nil
	}
	other := cfg
	other.Hub.URL = "https://elsewhere.example"
	if c := lvl(t, termRun(t, e, o, other, ""), "board token"); c.Level != Warn || !strings.Contains(c.Detail, "ignores") {
		t.Errorf("other hub: %+v", c)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(board.TokenFile(e.dirs.State.Path), 0o644); err != nil {
			t.Fatal(err)
		}
		c := lvl(t, termRun(t, e, o, cfg, ""), "board token")
		if c.Level != Warn || !strings.Contains(c.Detail, "0644") || !strings.Contains(c.Fix, "chmod 600") {
			t.Errorf("mode 0644: %+v", c)
		}
	}
}

func TestValidTitleOutput(t *testing.T) {
	good := []string{`{"terminalSequence":"\u001b]2;work · done\u0007"}` + "\n"}
	bad := []string{"", "\n", "hello", `{"terminalSequence":"x"}`, `{"terminalSequence":"\u001b]2;a\u0007"}{"terminalSequence":"\u001b]2;a\u0007"}`,
		`{"terminalSequence":"\u001b]2;a\u0007","extra":1}`, `{"terminalSequence":5}`}
	for _, s := range good {
		if err := validTitleOutput(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range bad {
		if validTitleOutput(s) == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestTitles(t *testing.T) {
	e := newFakeHome(t, "{}")
	o := termOpts(e)
	cfg := config.Defaults()
	cfg.Terminal.Titles = true
	target := filepath.Join(e.home, ".claude", "settings.json")

	// Titles on, nothing recorded.
	rep := termRun(t, e, o, cfg, "")
	if c := lvl(t, rep, "titles"); c.Level != Warn {
		t.Errorf("no targets: %+v", c)
	}
	// The embedded plugin has the title entries and the fixture prints valid output.
	if c := lvl(t, rep, "title hooks"); c.Level != OK {
		t.Errorf("title hooks: %+v", c)
	}
	if c := lvl(t, rep, "title output"); c.Level != OK {
		t.Errorf("title output: %+v", c)
	}

	setLedger(t, e, model.Ledger{V: 1, Terminal: &model.LedgerTerminal{Titles: &model.LedgerTermTargets{Targets: []string{target}}}})
	if c := lvl(t, termRun(t, e, o, cfg, ""), "titles"); c.Level != Warn || !strings.Contains(c.Detail, "1 of 1") {
		t.Errorf("env missing: %+v", c)
	}
	write(t, target, `{"env":{"CLAUDE_CODE_DISABLE_TERMINAL_TITLE":"1"}}`)
	if c := lvl(t, termRun(t, e, o, cfg, ""), "titles"); c.Level != OK {
		t.Errorf("env set: %+v", c)
	}

	// An installed plugin without the entries warns.
	mkt := filepath.Join(e.dirs.State.Path, "plugin", "marketplace")
	write(t, filepath.Join(mkt, "plugins", "watchyard", "hooks", "hooks.json"), `{"hooks":{}}`)
	setLedger(t, e, model.Ledger{V: 1, Plugin: &model.LedgerPlugin{Marketplace: mkt, ID: "watchyard@wy-local"},
		Terminal: &model.LedgerTerminal{Titles: &model.LedgerTermTargets{Targets: []string{target}}}})
	if c := lvl(t, termRun(t, e, o, cfg, ""), "title hooks"); c.Level != Warn {
		t.Errorf("hooks without entries: %+v", c)
	}

	// Titles off: no title lines even with a ledger entry.
	cfg.Terminal.Titles = false
	if rep := termRun(t, e, o, cfg, ""); has(rep, "titles") || has(rep, "title output") {
		t.Errorf("titles off: %+v", rep.Checks)
	}
}

func TestPane(t *testing.T) {
	e := newFakeHome(t, "{}")
	o := termOpts(e)
	cfg := config.Defaults()
	t1 := filepath.Join(e.home, ".claude", "settings.json")
	t2 := filepath.Join(e.home, ".claude-4", "settings.json")
	setLedger(t, e, model.Ledger{V: 1, Terminal: &model.LedgerTerminal{Pane: &model.LedgerTermTargets{Targets: []string{t1, t2}}}})
	on := `{"enabledPlugins":{"` + plan.PaneID + `":true}}`

	write(t, t1, on)
	if c := lvl(t, termRun(t, e, o, cfg, "2.1.296 (Claude Code)"), "pane"); c.Level != Warn || !strings.Contains(c.Detail, "1 of 2") {
		t.Errorf("one target off: %+v", c)
	}
	write(t, t2, on)
	if c := lvl(t, termRun(t, e, o, cfg, "2.1.296 (Claude Code)"), "pane"); c.Level != OK {
		t.Errorf("all on: %+v", c)
	}
	if c := lvl(t, termRun(t, e, o, cfg, "2.1.100 (Claude Code)"), "pane"); c.Level != Warn || !strings.Contains(c.Detail, "older") {
		t.Errorf("old claude: %+v", c)
	}
	if c := lvl(t, termRun(t, e, o, cfg, ""), "pane"); c.Level != OK {
		t.Errorf("unknown version must not warn here: %+v", c)
	}
}

func TestWarpPlugin(t *testing.T) {
	e := newFakeHome(t, "{}")
	o := termOpts(e)
	cfg := config.Defaults()
	target := filepath.Join(e.home, ".claude", "settings.json")
	root, co := t.TempDir(), t.TempDir()
	old, cur := strings.Repeat("a", 40), strings.Repeat("b", 40)
	write(t, filepath.Join(root, "known_marketplaces.json"), `{"`+plan.WarpMarketplace+`":{"installLocation":"`+filepath.ToSlash(co)+`"}}`)
	write(t, filepath.Join(co, ".git", "HEAD"), cur+"\n")
	led := func(commit string) {
		setLedger(t, e, model.Ledger{V: 1, Terminal: &model.LedgerTerminal{WarpPlugin: &model.LedgerWarpPlugin{
			Roots: []string{root}, Targets: []string{target}, Ref: "main", Commit: commit}}})
	}

	led(cur)
	if c := lvl(t, termRun(t, e, o, cfg, ""), "warp plugin"); c.Level != Warn {
		t.Errorf("not enabled: %+v", c)
	}
	write(t, target, `{"enabledPlugins":{"`+plan.WarpPluginID+`":true}}`)
	rep := termRun(t, e, o, cfg, "")
	if c := lvl(t, rep, "warp plugin"); c.Level != OK {
		t.Errorf("enabled: %+v", c)
	}
	if c := lvl(t, rep, "warp plugin commit"); c.Level != OK {
		t.Errorf("same commit: %+v", c)
	}
	if has(rep, "warp plugin jq") {
		t.Error("jq is on PATH: no jq line")
	}

	led(old)
	if c := lvl(t, termRun(t, e, o, cfg, ""), "warp plugin commit"); c.Level != Warn || !strings.Contains(c.Detail, "checkout moved") {
		t.Errorf("moved: %+v", c)
	}

	o.LookPath = func(n string) (string, error) {
		if n == "jq" {
			return "", errors.New("not found")
		}
		return "/fake/bin/" + n, nil
	}
	if c := lvl(t, termRun(t, e, o, cfg, ""), "warp plugin jq"); c.Level != Warn {
		t.Errorf("no jq: %+v", c)
	}
}

func TestTabs(t *testing.T) {
	e := newFakeHome(t, "{}")
	o := termOpts(e)
	cfg := config.Defaults()
	dir := t.TempDir()
	launcher := filepath.Join(dir, "claude-2")
	write(t, launcher, "#!/bin/sh\n")
	content := warp.Render(warp.Spec{Slot: "~/.claude-2", Label: "work", Command: launcher})
	path := warp.TabPath(dir, "work")
	write(t, path, string(content))
	setLedger(t, e, model.Ledger{V: 1, Terminal: &model.LedgerTerminal{Tabs: []model.LedgerTab{
		{Path: path, SHA256: warp.Hash(content), Label: "work"}}}})

	if c := lvl(t, termRun(t, e, o, cfg, ""), "tab config work"); c.Level != OK || !strings.Contains(c.Detail, launcher) {
		t.Errorf("fine: %+v", c)
	}
	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if c := lvl(t, termRun(t, e, o, cfg, ""), "tab config work"); c.Level != Warn || !strings.Contains(c.Detail, "does not exist") {
		t.Errorf("launcher gone: %+v", c)
	}
	write(t, path, string(content)+"# mine\n")
	if c := lvl(t, termRun(t, e, o, cfg, ""), "tab config work"); c.Level != OK || !strings.Contains(c.Detail, "yours now") {
		t.Errorf("edited: %+v", c)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if c := lvl(t, termRun(t, e, o, cfg, ""), "tab config work"); c.Level != OK || !strings.Contains(c.Detail, "gone") {
		t.Errorf("file gone: %+v", c)
	}
}

func TestWarpDetection(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("fixture lays out the Linux data folder")
	}
	e := newFakeHome(t, "{}")
	o := termOpts(e)
	cfg := config.Defaults()
	env := map[string]string{"XDG_DATA_HOME": filepath.Join(e.home, "data")}
	o.Getenv = func(k string) string { return env[k] }

	if rep := termRun(t, e, o, cfg, ""); has(rep, "warp") {
		t.Fatal("no Warp here: no line")
	}
	if err := os.MkdirAll(filepath.Join(env["XDG_DATA_HOME"], "warp-terminal"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := lvl(t, termRun(t, e, o, cfg, ""), "warp")
	if c.Level != OK || !strings.Contains(c.Detail, "stable") {
		t.Errorf("installed: %+v", c)
	}

	env["TERM_PROGRAM"] = "WarpTerminal"
	env["WARP_FOCUS_URL"] = "warp://session/0123456789abcdef0123456789abcdef"
	if rep := termRun(t, e, o, cfg, ""); has(rep, "warp focus url") {
		t.Errorf("valid url: %+v", rep.Checks)
	}
	env["WARP_FOCUS_URL"] = "warp://pane/xyz"
	if c := lvl(t, termRun(t, e, o, cfg, ""), "warp focus url"); c.Level != Warn {
		t.Errorf("drift: %+v", c)
	}
	// Remote shell inside a forwarded Warp: no URL is expected.
	env["WARP_FOCUS_URL"] = ""
	env["SSH_CONNECTION"] = "1.2.3.4 5 6.7.8.9 22"
	if rep := termRun(t, e, o, cfg, ""); has(rep, "warp focus url") {
		t.Errorf("ssh without url: %+v", rep.Checks)
	}
}

func TestAcceptEnvNote(t *testing.T) {
	e := newFakeHome(t, "{}")
	o := termOpts(e)
	cfg := config.Defaults()
	line := func(term string) string {
		return `{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T9B4CD","kind":"hook_event","ts":"2026-10-12T08:00:00.000Z","machine":"m",` + term + `"payload":{"session_id":"s1","hook_event_name":"Stop"}}` + "\n"
	}
	day := filepath.Join(e.dirs.State.Path, "outbox", "2026-10-12.jsonl")
	env := map[string]string{}
	o.Getenv = func(k string) string { return env[k] }

	write(t, day, line(`"term":{"app":"warp","id":"abc"},`))
	if rep := termRun(t, e, o, cfg, ""); has(rep, "ssh env") {
		t.Error("not over SSH: no note")
	}
	env["SSH_CONNECTION"] = "1.2.3.4 5 6.7.8.9 22"
	c := lvl(t, termRun(t, e, o, cfg, ""), "ssh env")
	if c.Level != Warn || !strings.Contains(c.Fix, "AcceptEnv LC_WY_*") {
		t.Errorf("warp over ssh without focus: %+v", c)
	}
	write(t, day, line(`"term":{"app":"warp","id":"abc","focus_url":"warp://session/0123456789abcdef0123456789abcdef"},`))
	if rep := termRun(t, e, o, cfg, ""); has(rep, "ssh env") {
		t.Error("focus url arrived: no note")
	}
	write(t, day, line(`"term":{"app":"ghostty"},`))
	if rep := termRun(t, e, o, cfg, ""); has(rep, "ssh env") {
		t.Error("not Warp: no note")
	}
}
