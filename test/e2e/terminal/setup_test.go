package terminal

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/testtree"
	"github.com/mraj602/switchyard/internal/warp"
)

// labelled gives the laptop's accounts the names the scenarios use: ~/.claude-2 is "work"
// and ~/.claude-3 "personal", each with a launcher on PATH (stubs: the tab configs run them
// by path and sy open lists them), and makes Warp's data folder exist (Linux layout).
func (l *laptop) labelled() {
	l.t.Helper()
	l.must("config", "set", "slots.labels.~/.claude-2", "work")
	l.must("config", "set", "slots.labels.~/.claude-3", "personal")
	for _, n := range []string{"claude-2", "claude-3"} {
		writeFile(l.t, l.h.Abs(".local/bin/"+n), "#!/bin/sh\necho \"launched $0 $*\"\n", 0o755)
	}
	mkdirs(l.t, l.h.Abs(".local/share/warp-terminal"))
}

func (l *laptop) tabsDir() string { return l.h.Abs(".local/share/warp-terminal/tab_configs") }

func (l *laptop) ledgerTerminal() map[string]any {
	l.t.Helper()
	b, err := os.ReadFile(ledger.Path(l.stateDir()))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		l.t.Fatal(err)
	}
	var led struct {
		Terminal map[string]any `json:"terminal"`
	}
	if err := json.Unmarshal(b, &led); err != nil {
		l.t.Fatal(err)
	}
	return led.Terminal
}

// symlinks lists the links under root with their targets.
func symlinks(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink != 0 {
			out[p], _ = os.Readlink(p)
		}
		return nil
	})
	return out
}

// filesWith lists the files under dir whose content has the text.
func filesWith(dir, text string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), text) {
				out = append(out, p)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

const titleKey = "CLAUDE_CODE_DISABLE_TERMINAL_TITLE"

// E8: titles. `sy term titles on --yes` puts the env key in the shared settings target (and
// so through the links of -2 and -3, which stay links) and refreshes the hooks plugin;
// `sy hook --title Stop` with a question prints the terminalSequence JSON; `sy setup
// --undo` gives the home back byte for byte.
func TestE8Titles(t *testing.T) {
	l := newLaptop(t)
	l.setup()
	l.labelled()
	tree0 := testtree.Take(t, l.home, snapOpts)
	links0 := symlinks(l.home)
	if len(links0) == 0 {
		t.Fatal("the laptop fixture should share settings by links")
	}
	market := filepath.Join(l.stateDir(), "plugin", "marketplace")
	// A sy-local written by an older sy: no title entries in the hooks plugin. `titles on`
	// rewrites the marketplace and updates the plugin so the title hooks run.
	hp := filepath.Join(market, "plugins", "switchyard", "hooks", "hooks.json")
	old := readFile(hp)
	if !strings.Contains(old, "hook --title ") {
		t.Fatalf("the hooks plugin has no title entries to strip:\n%s", old)
	}
	writeFile(t, hp, strings.ReplaceAll(old, "hook --title ", "hook "), 0o644)
	writeFile(t, filepath.Join(market, "stale.txt"), "old", 0o644)
	_ = os.Remove(l.wrapLog)
	tree0 = testtree.Take(t, l.home, snapOpts)

	// Off: nothing is printed.
	payload := func(event string, fields map[string]any) string {
		p := map[string]any{"session_id": "e8000000-0000-4000-8000-000000000001", "hook_event_name": event, "cwd": "/home/u/api"}
		for k, v := range fields {
			p[k] = v
		}
		b, _ := json.Marshal(p)
		return string(b)
	}
	question := payload("Stop", map[string]any{"last_assistant_message": "Updated the migration.\n\nShould I keep the old column?"})
	if r := l.run(question, l.slot(2), "hook", "--title", "Stop"); r.code != 0 || r.out != "" {
		t.Errorf("--title with titles off: exit %d, stdout %q", r.code, r.out)
	}

	out := l.must("term", "titles", "on", "--yes")
	if !strings.Contains(out, titleKey) {
		t.Errorf("titles on does not mention %s:\n%s", titleKey, out)
	}
	for _, rel := range []string{".claude/settings.json", ".claude-2/settings.json", ".claude-3/settings.json"} {
		if !strings.Contains(readFile(l.h.Abs(rel)), `"`+titleKey+`": "1"`) {
			t.Errorf("%s lacks the env key:\n%s", rel, readFile(l.h.Abs(rel)))
		}
	}
	for _, rel := range []string{".claude-2/settings.json", ".claude-3/settings.json"} {
		if fi, err := os.Lstat(l.h.Abs(rel)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is no longer a link: %v", rel, err)
		}
	}
	if got := symlinks(l.home); len(got) != len(links0) {
		t.Errorf("links changed: %d before, %d after", len(links0), len(got))
	}
	// The hooks plugin in sy-local carries the title entry of every event of T6.
	hooksJSON := readFile(hp)
	if _, err := os.Stat(filepath.Join(market, "stale.txt")); err == nil {
		t.Error("the marketplace was not rewritten (stale file left)")
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PermissionRequest", "Notification", "Stop", "StopFailure", "SessionEnd"} {
		if !strings.Contains(hooksJSON, "hook --title "+ev) {
			t.Errorf("hooks.json has no `hook --title %s` entry", ev)
		}
	}
	runs := strings.Join(l.wrapRuns(), "\n")
	if !strings.Contains(runs, "plugin marketplace update sy-local") || !strings.Contains(runs, "plugin update switchyard@sy-local") {
		t.Errorf("the hooks plugin was not refreshed through claude:\n%s", runs)
	}
	if lt := l.ledgerTerminal(); lt["titles"] == nil {
		t.Errorf("ledger terminal block: %v", lt)
	}

	// The hook: exactly the JSON of T6, once.
	const want = `{"terminalSequence":"\u001b]2;work · api · asked\u0007"}`
	r := l.run(question, l.slot(2), "hook", "--title", "Stop")
	if r.code != 0 || strings.TrimSpace(r.out) != want {
		t.Fatalf("sy hook --title Stop: exit %d\n got %q\nwant %q\nstderr %s", r.code, r.out, want, r.err)
	}
	if r := l.run(question, l.slot(2), "hook", "--title", "Stop"); r.out != "" {
		t.Errorf("an unchanged title was printed again: %q", r.out)
	}
	r = l.run(payload("UserPromptSubmit", map[string]any{"prompt": "yes"}), l.slot(2), "hook", "--title", "UserPromptSubmit")
	if strings.TrimSpace(r.out) != `{"terminalSequence":"\u001b]2;work · api · working\u0007"}` {
		t.Errorf("next title: %q", r.out)
	}
	// A slot with another label gets its own words.
	r = l.run(payload("Stop", map[string]any{"session_id": "e8000000-0000-4000-8000-000000000002", "last_assistant_message": "Done."}), l.slot(3), "hook", "--title", "Stop")
	if strings.TrimSpace(r.out) != `{"terminalSequence":"\u001b]2;personal · api · done\u0007"}` {
		t.Errorf("slot 3 title: %q", r.out)
	}
	// The plugin's hook command is the same entry Claude Code runs.
	if h := filesWith(market, "hook --title"); len(h) == 0 {
		t.Errorf("no plugin file runs `hook --title`")
	}

	l.must("setup", "--undo", "--yes")
	testtree.Equal(t, tree0, testtree.Take(t, l.home, snapOpts))
	if got := strings.TrimSpace(l.must("config", "get", "terminal.titles")); got != "false" {
		t.Errorf("terminal.titles after undo = %q", got)
	}
	if lt := l.ledgerTerminal(); lt != nil {
		t.Errorf("ledger terminal block after undo: %v", lt)
	}
	if r := l.run(question, l.slot(2), "hook", "--title", "Stop"); r.out != "" {
		t.Errorf("--title still prints after the undo: %q", r.out)
	}
}

var colourLine = regexp.MustCompile(`(?m)^color = "([a-z]+)"$`)

// E9: Warp tabs. With a Warp data folder (Linux layout) `sy term warp on --yes` writes three
// tab configs with the account colours and launchers; a second run changes nothing; `sy
// open work` hands the opener warp://tab_config/switchyard-work; `off` removes what sy
// wrote except a file edited by hand.
func TestE9WarpTabs(t *testing.T) {
	l := newLaptop(t)
	l.setup()
	l.labelled()
	// Without Warp's folder sy refuses (the folder is made here, in labelled).
	if err := os.RemoveAll(l.h.Abs(".local/share/warp-terminal")); err != nil {
		t.Fatal(err)
	}
	if r := l.sy("term", "warp", "on", "--yes"); r.code != 4 || !strings.Contains(r.err, "Warp not found") {
		t.Fatalf("no Warp: exit %d %s", r.code, r.err)
	}
	mkdirs(t, l.h.Abs(".local/share/warp-terminal"))
	tree0 := testtree.Take(t, l.home, snapOpts)

	out := l.must("term", "warp", "on", "--yes")
	ents, err := os.ReadDir(l.tabsDir())
	if err != nil || len(ents) != 3 {
		t.Fatalf("tab configs: %v %v\n%s", ents, err, out)
	}
	type tab struct{ slot, label, launcher, colour string }
	tabs := []tab{
		{"~/.claude", "claude", l.h.Abs(".local/bin/claude"), warp.Nearest("#14b8a6")},
		{"~/.claude-2", "work", l.h.Abs(".local/bin/claude-2"), warp.Nearest("#f59e0b")},
		{"~/.claude-3", "personal", l.h.Abs(".local/bin/claude-3"), warp.Nearest("#8b5cf6")},
	}
	if tabs[1].colour != "yellow" {
		t.Errorf("the amber of ~/.claude-2 maps to %q, want yellow (terminal.md T8)", tabs[1].colour)
	}
	for _, tb := range tabs {
		path := filepath.Join(l.tabsDir(), "switchyard-"+tb.label+".toml")
		b := readFile(path)
		if !strings.HasPrefix(b, "# sy-warp-tab v1 slot="+tb.slot+" label="+tb.label+" (") {
			t.Errorf("%s header:\n%s", path, b)
		}
		if m := colourLine.FindStringSubmatch(b); m == nil || m[1] != tb.colour {
			t.Errorf("%s colour %v, want %s\n%s", path, m, tb.colour, b)
		}
		if !strings.Contains(b, `commands = ["`+tb.launcher+`"]`) {
			t.Errorf("%s launcher, want %s:\n%s", path, tb.launcher, b)
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
			t.Errorf("%s mode %v %v", path, fi, err)
		}
	}
	if lt := l.ledgerTerminal(); lt["tabs"] == nil {
		t.Errorf("ledger terminal block: %v", lt)
	}

	// A second run: nothing to do.
	if again := l.must("term", "warp", "on", "--yes"); !strings.Contains(again, "0 changes") {
		t.Errorf("second run:\n%s", again)
	}

	// sy open, in Warp: the tab config URI goes to the opener.
	inWarp := []string{"TERM_PROGRAM=WarpTerminal"}
	if r := l.run("", inWarp, "open", "work"); r.code != 0 {
		t.Fatalf("sy open work: exit %d %s%s", r.code, r.out, r.err)
	}
	if got := l.openerRecords(); !sameArgv(got, openerArgv("warp://tab_config/switchyard-work")) {
		t.Errorf("sy open work: opener got %v", got)
	}

	// Undo of the `on` gives the home back (no tab_configs folder, no files); on again.
	l.must("setup", "--undo", "--yes")
	testtree.Equal(t, tree0, testtree.Take(t, l.home, snapOpts))
	if lt := l.ledgerTerminal(); lt != nil {
		t.Errorf("ledger terminal block after undo: %v", lt)
	}
	l.must("term", "warp", "on", "--yes")
	if ents, err := os.ReadDir(l.tabsDir()); err != nil || len(ents) != 3 {
		t.Fatalf("tab configs after on again: %v %v", ents, err)
	}

	// Edit one by hand; off removes the others and says so.
	personal := filepath.Join(l.tabsDir(), "switchyard-personal.toml")
	edited := "# sy-warp-tab v1 slot=~/.claude-3 label=personal (mine now)\nname = \"mine\"\n"
	writeFile(t, personal, edited, 0o644)
	off := l.must("term", "warp", "off", "--yes")
	if !strings.Contains(off, "switchyard-personal.toml was edited") {
		t.Errorf("off did not report the edited file:\n%s", off)
	}
	if readFile(personal) != edited {
		t.Errorf("the edited tab config changed:\n%s", readFile(personal))
	}
	left, _ := os.ReadDir(l.tabsDir())
	if len(left) != 1 || left[0].Name() != "switchyard-personal.toml" {
		t.Errorf("after off: %v", left)
	}
	if r := l.run("", inWarp, "open", "work"); r.code != 4 || !strings.Contains(r.err, "sy term warp on") {
		t.Errorf("open without a tab config: exit %d %s", r.code, r.err)
	}
}

// E10: uninstall. After titles, Warp tabs, the pane and Warp's plugin, `sy uninstall --yes`
// leaves no terminal piece and no ledger terminal block; the argv log of the fake claude
// shows the uninstalls, the pane's before the marketplace goes.
func TestE10Uninstall(t *testing.T) {
	l := newLaptop(t)
	l.setup()
	l.labelled()
	l.must("term", "titles", "on", "--yes")
	l.must("term", "warp", "on", "--yes")
	l.must("term", "setup", "--pane", "--warp-plugin", "--yes")
	lt := l.ledgerTerminal()
	for _, k := range []string{"titles", "tabs", "pane", "warp_plugin"} {
		if lt[k] == nil {
			t.Fatalf("ledger terminal block lacks %q: %v", k, lt)
		}
	}
	if len(l.wrapRuns()) == 0 {
		t.Fatal("no plugin runs logged")
	}
	_ = os.Remove(l.wrapLog)
	claudeLog := filepath.Join(l.syHome, "claude.log")
	_ = os.Remove(claudeLog)

	out := l.must("uninstall", "--yes")
	runs := l.wrapRuns()
	joined := strings.Join(runs, "\n")
	pane, mkt := -1, -1
	for i, r := range runs {
		if strings.Contains(r, "plugin uninstall switchyard-pane@sy-local") {
			pane = i
		}
		if strings.Contains(r, "plugin marketplace remove sy-local") {
			mkt = i
		}
	}
	if pane < 0 || mkt < 0 || pane > mkt {
		t.Errorf("the pane must be uninstalled before sy-local goes (pane %d, marketplace %d):\n%s\n%s", pane, mkt, joined, out)
	}
	for _, w := range []string{"plugin uninstall warp@claude-code-warp", "plugin marketplace remove claude-code-warp"} {
		if !strings.Contains(joined, w) {
			t.Errorf("uninstall did not run %q:\n%s", w, joined)
		}
	}
	// The fake claude's own log agrees.
	fl := readFile(claudeLog)
	for _, w := range []string{`"uninstall"`, "switchyard-pane@sy-local", "warp@claude-code-warp"} {
		if !strings.Contains(fl, w) {
			t.Errorf("fakeclaude log lacks %s:\n%s", w, fl)
		}
	}

	if lt := l.ledgerTerminal(); lt != nil {
		t.Errorf("ledger terminal block after uninstall: %v", lt)
	}
	for _, rel := range []string{".claude/settings.json", ".claude-2/settings.json", ".claude-3/settings.json"} {
		if strings.Contains(readFile(l.h.Abs(rel)), titleKey) {
			t.Errorf("%s still has the titles key", rel)
		}
	}
	if ents, _ := os.ReadDir(l.tabsDir()); len(ents) != 0 {
		t.Errorf("tab configs left: %v", ents)
	}
	if got := strings.TrimSpace(l.sy("config", "get", "terminal.titles").out); got == "true" {
		t.Errorf("terminal.titles is still on")
	}
	for _, p := range []string{"term", "board"} {
		if _, err := os.Stat(filepath.Join(l.stateDir(), p)); err == nil {
			t.Logf("note: <state>/%s remains after uninstall", p)
		}
	}
}
