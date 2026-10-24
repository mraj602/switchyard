package main

// End-to-end tests of `wy term` and `wy open`: the wy binary
// against fakeclaude in a temp home. A wrapper `claude` in front of fakeclaude records
// every argv with its CLAUDE_CONFIG_DIR and points Warp's GitHub marketplace at a local
// copy; fake xdg-open and open (macOS) record the URI wy open hands it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/testtree"
)

type termHome struct {
	*setupHome
	h                *detecttest.Home
	wrapLog, openLog string
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTermHome is h after `wy setup --yes --hub later --named-launchers`, with the wrapper
// claude and fake xdg-open first on PATH.
func newTermHome(t *testing.T, h *detecttest.Home) *termHome {
	t.Helper()
	return newTermHomeWith(t, h, "setup", "--yes", "--hub", "later", "--named-launchers")
}

// newTermHomeWith is newTermHome with its own wy setup argv (none: no setup run).
func newTermHomeWith(t *testing.T, h *detecttest.Home, setup ...string) *termHome {
	t.Helper()
	s := newSetupHome(t, h)
	th := &termHome{setupHome: s, h: h, wrapLog: filepath.Join(s.wyHome, "wrap.log"), openLog: filepath.Join(s.wyHome, "open.log")}
	bin := filepath.Join(s.wyHome, "wrapbin")
	src := filepath.Join(s.wyHome, "warp-src")
	writeFile(t, filepath.Join(src, ".claude-plugin", "marketplace.json"),
		`{"name":"claude-code-warp","owner":{"name":"warp"},"plugins":[{"name":"warp","source":"./plugins/warp"}]}`)
	writeFile(t, filepath.Join(src, "plugins", "warp", ".claude-plugin", "plugin.json"), `{"name":"warp","version":"1.0.0"}`)
	writeExec(t, filepath.Join(bin, "claude"), `#!/bin/sh
printf '%s|%s\n' "${CLAUDE_CONFIG_DIR-<unset>}" "$*" >> "`+th.wrapLog+`"
if [ "$1 $2 $3 $4" = "plugin marketplace add warpdotdev/claude-code-warp#main" ]; then
  shift 4
  exec "`+filepath.Join(fakeClaudeDir(t), "claude")+`" plugin marketplace add "`+src+`" "$@"
fi
exec "`+filepath.Join(fakeClaudeDir(t), "claude")+`" "$@"
`)
	opener := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + th.openLog + "\"\n"
	writeExec(t, filepath.Join(bin, "xdg-open"), opener)
	writeExec(t, filepath.Join(bin, "open"), opener) // macOS
	for i, kv := range s.env {
		if strings.HasPrefix(kv, "PATH=") {
			s.env[i] = "PATH=" + bin + ":" + strings.TrimPrefix(kv, "PATH=")
		}
	}
	if len(setup) > 0 {
		out, errOut, code := s.wy(t, "", setup...)
		if code != 0 {
			t.Fatalf("setup: exit %d\n%s\n%s", code, out, errOut)
		}
	}
	return th
}

func (th *termHome) setenv(kv string) {
	k, _, _ := strings.Cut(kv, "=")
	for i, e := range th.env {
		if strings.HasPrefix(e, k+"=") {
			th.env[i] = kv
			return
		}
	}
	th.env = append(th.env, kv)
}

func (th *termHome) runs(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(th.wrapLog)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.Contains(l, "|plugin ") && !strings.Contains(l, " list") {
			out = append(out, l)
		}
	}
	return out
}

func (th *termHome) ledgerTerm(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(ledger.Path(th.state()))
	if err != nil {
		t.Fatal(err)
	}
	var l struct {
		Terminal map[string]any `json:"terminal"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	return l.Terminal
}

// links lists every symlink under root with its target.
func links(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type()&os.ModeSymlink != 0 {
			out[p], _ = os.Readlink(p)
		}
		return nil
	})
	return out
}

// Nothing outside the temp roots changes: the parent of the home and of WY_HOME list the
// same entries before and after.
func parentSnap(t *testing.T, dirs ...string) []string {
	t.Helper()
	var out []string
	for _, d := range dirs {
		ents, _ := os.ReadDir(filepath.Dir(d))
		for _, e := range ents {
			out = append(out, filepath.Join(filepath.Dir(d), e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func TestTermUsage(t *testing.T) {
	checkUsage(t, []string{"term"}, []string{"term", "nope"}, []string{"term", "titles"}, []string{"term", "warp", "maybe"},
		[]string{"term", "setup"}, []string{"term", "warp", "on", "--channel", "beta"}, []string{"term", "status", "x"},
		[]string{"open", "a", "b"})
}

// Titles on, a second on (0 changes, no backup set), off, then undo of each restores the
// tree; the shared settings links stay links.
func TestTermTitlesOnOffUndo(t *testing.T) {
	th := newTermHome(t, detecttest.Laptop(t))
	before := parentSnap(t, th.root, th.wyHome)
	tree0 := testtree.Take(t, th.root, snapOpts)
	links0 := links(t, th.root)
	if len(links0) == 0 {
		t.Fatal("the fixture should share settings by links")
	}
	out := th.mustWy(t, "term", "titles", "on", "--yes")
	if !strings.Contains(out, "CLAUDE_CODE_DISABLE_TERMINAL_TITLE") {
		t.Errorf("on:\n%s", out)
	}
	b, _ := os.ReadFile(th.h.Abs(".claude/settings.json"))
	if !strings.Contains(string(b), `"CLAUDE_CODE_DISABLE_TERMINAL_TITLE": "1"`) {
		t.Fatalf("settings:\n%s", b)
	}
	if lt := th.ledgerTerm(t); lt["titles"] == nil {
		t.Fatalf("ledger %v", lt)
	}
	sets, _ := os.ReadDir(filepath.Join(th.state(), "backup"))
	out = th.mustWy(t, "term", "titles", "on", "--yes", "--json")
	var p struct {
		Summary struct {
			Changes int `json:"changes"`
		} `json:"summary"`
		Actions []any `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.Summary.Changes != 0 || len(p.Actions) != 0 {
		t.Fatalf("second on: %v\n%s", err, out)
	}
	if again, _ := os.ReadDir(filepath.Join(th.state(), "backup")); len(again) != len(sets) {
		t.Errorf("second on made a backup set")
	}
	if !strings.Contains(th.mustWy(t, "term", "titles", "on", "--yes"), "0 changes") {
		t.Errorf("text second on does not say 0 changes")
	}
	if got := links(t, th.root); len(got) != len(links0) {
		t.Fatalf("links changed: %v", got)
	}
	th.mustWy(t, "setup", "--undo", "--yes")
	testtree.Equal(t, tree0, testtree.Take(t, th.root, snapOpts))
	if lt := th.ledgerTerm(t); lt != nil {
		t.Errorf("ledger after undo: %v", lt)
	}

	th.mustWy(t, "term", "titles", "on", "--yes")
	on := testtree.Take(t, th.root, snapOpts)
	th.mustWy(t, "term", "titles", "off", "--yes")
	b, _ = os.ReadFile(th.h.Abs(".claude/settings.json"))
	if strings.Contains(string(b), "CLAUDE_CODE_DISABLE_TERMINAL_TITLE") {
		t.Fatalf("off left the key:\n%s", b)
	}
	th.mustWy(t, "setup", "--undo", "--yes")
	testtree.Equal(t, on, testtree.Take(t, th.root, snapOpts))
	th.mustWy(t, "term", "off", "--yes")
	if got := links(t, th.root); len(got) != len(links0) {
		t.Fatalf("links changed: %v", got)
	}
	if after := parentSnap(t, th.root, th.wyHome); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("something was written next to the temp roots:\n%v\n%v", before, after)
	}
}

// The pane and Warp's plugin: every `claude plugin` run goes to the fake as the right
// folder; undo restores the tree; off removes what wy added.
func TestTermPluginsOnUndoOff(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	th := newTermHome(t, h)
	tree0 := testtree.Take(t, th.root, snapOpts)
	_ = os.Remove(th.wrapLog)
	out := th.mustWy(t, "term", "setup", "--pane", "--warp-plugin", "--yes")
	if !strings.Contains(out, "branch main") {
		t.Errorf("no branch warning:\n%s", out)
	}
	// wy setup shared ~/.claude-2's settings and plugins with ~/.claude: one root, one
	// target, so each run happens once, as ~/.claude (CLAUDE_CONFIG_DIR unset).
	runs := th.runs(t)
	if len(runs) != 3 {
		t.Errorf("runs:\n%s", strings.Join(runs, "\n"))
	}
	want := map[string]bool{
		"<unset>|plugin install watchyard-pane@wy-local --json":                  false,
		"<unset>|plugin marketplace add warpdotdev/claude-code-warp#main --json": false,
		"<unset>|plugin install warp@claude-code-warp --json":                    false,
	}
	for _, r := range runs {
		if _, ok := want[r]; ok {
			want[r] = true
		}
	}
	for r, seen := range want {
		if !seen {
			t.Errorf("not run: %s\nruns:\n%s", r, strings.Join(runs, "\n"))
		}
	}
	lt := th.ledgerTerm(t)
	if lt["pane"] == nil || lt["warp_plugin"] == nil {
		t.Fatalf("ledger %v", lt)
	}
	if wp := lt["warp_plugin"].(map[string]any); wp["ref"] != "main" {
		t.Errorf("warp plugin %v", wp)
	}
	st := th.mustWy(t, "term", "status")
	if !strings.Contains(st, "Pane         on") || !strings.Contains(st, "Warp plugin  on") {
		t.Errorf("status:\n%s", st)
	}
	th.mustWy(t, "setup", "--undo", "--yes")
	testtree.Equal(t, tree0, testtree.Take(t, th.root, snapOpts))

	th.mustWy(t, "term", "setup", "--pane", "--warp-plugin", "--yes")
	_ = os.Remove(th.wrapLog)
	th.mustWy(t, "term", "off", "--yes")
	joined := strings.Join(th.runs(t), "\n")
	for _, w := range []string{"plugin uninstall watchyard-pane@wy-local", "plugin uninstall warp@claude-code-warp", "plugin marketplace remove claude-code-warp"} {
		if !strings.Contains(joined, w) {
			t.Errorf("off did not run %q:\n%s", w, joined)
		}
	}
	if lt := th.ledgerTerm(t); lt != nil {
		t.Errorf("ledger after off: %v", lt)
	}
}

// Tab configs: written per account with the marker; an edited one survives off and is
// reported; a watchyard-x.toml without the marker is never touched; wy open in Warp hands
// the opener warp://tab_config/watchyard-<label>.
func TestTermWarpTabsAndOpen(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	h.B.File(".claude-3/.claude.json", `{}`, 0o600)
	th := newTermHome(t, h)
	before := parentSnap(t, th.root, th.wyHome)
	_, errOut, code := th.wy(t, "", "term", "warp", "on", "--yes")
	if code != 4 || !strings.Contains(errOut, "Warp not found") {
		t.Fatalf("no Warp: exit %d %s", code, errOut)
	}
	folder := filepath.Join(h.Abs(warpDataRel()), "tab_configs")
	if err := os.MkdirAll(filepath.Dir(folder), 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(folder, "watchyard-x.toml")
	tree0 := testtree.Take(t, th.root, snapOpts)
	out := th.mustWy(t, "term", "warp", "on", "--yes")
	work := filepath.Join(folder, "watchyard-claude-2.toml")
	b, err := os.ReadFile(work)
	if err != nil || !strings.HasPrefix(string(b), "# wy-warp-tab v1 slot=~/.claude-2 label=claude-2 (") ||
		!strings.Contains(string(b), `commands = ["`+h.Abs(".local/bin/claude-2")+`"]`) {
		t.Fatalf("tab config %v:\n%s\n%s", err, b, out)
	}
	th.mustWy(t, "setup", "--undo", "--yes")
	testtree.Equal(t, tree0, testtree.Take(t, th.root, snapOpts))

	writeFile(t, theirs, "name = \"mine\"\n")
	th.mustWy(t, "term", "warp", "on", "--yes")
	if again := th.mustWy(t, "term", "warp", "on", "--yes"); !strings.Contains(again, "0 changes") {
		t.Errorf("second on:\n%s", again)
	}
	st := th.mustWy(t, "term", "status")
	if !strings.Contains(st, "claude-2     ours") || !strings.Contains(st, "x            not-ours") {
		t.Errorf("status:\n%s", st)
	}

	// wy open: in Warp, the opener gets the URI; outside, the launcher runs (fake: prints argv).
	th.setenv("TERM_PROGRAM=WarpTerminal")
	out = th.mustWy(t, "open", "claude-2")
	if ob, _ := os.ReadFile(th.openLog); strings.TrimSpace(string(ob)) != "warp://tab_config/watchyard-claude-2" {
		t.Fatalf("opener got %q\n%s", ob, out)
	}
	if _, errOut, code := th.wy(t, "", "open", "x"); code != 2 {
		t.Errorf("open of a non-account: exit %d %s", code, errOut)
	}
	writeExec(t, h.Abs(".local/bin/claude-3"), "#!/bin/sh\necho \"launched $0 $*\"\n")
	if out, _, code := th.wy(t, "", "open", "claude-3", "--here"); code != 0 || !strings.Contains(out, "launched "+h.Abs(".local/bin/claude-3")) {
		t.Errorf("--here: exit %d %q", code, out)
	}
	th.setenv("TERM_PROGRAM=dumb")
	if out, _, code := th.wy(t, "", "open", "claude-3"); code != 0 || !strings.Contains(out, "launched "+h.Abs(".local/bin/claude-3")) {
		t.Errorf("outside Warp: exit %d %q", code, out)
	}
	_ = os.Remove(th.openLog)
	list := th.mustWy(t, "open")
	for _, l := range []string{"claude-2", "claude-3", "~/.claude-2"} {
		if !strings.Contains(list, l) {
			t.Errorf("list misses %s:\n%s", l, list)
		}
	}
	var lj struct {
		V        int `json:"v"`
		Accounts []struct {
			Label string `json:"label"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(th.mustWy(t, "open", "--json")), &lj); err != nil || lj.V != 1 || len(lj.Accounts) != 3 {
		t.Errorf("list json %v %+v", err, lj)
	}
	if _, err := os.Stat(th.openLog); err == nil {
		t.Error("wy open with no label opened something")
	}
	if _, errOut, code := th.wy(t, "", "open", "nobody"); code != 2 || !strings.Contains(errOut, "no account labelled") {
		t.Errorf("unknown label: exit %d %s", code, errOut)
	}

	// The person edits one; off leaves it and says so, and leaves theirs alone.
	edited := "# wy-warp-tab v1 slot=~/.claude-2 label=claude-2 (mine now)\nname = \"edited\"\n"
	writeFile(t, work, edited)
	out = th.mustWy(t, "term", "warp", "off", "--yes")
	if !strings.Contains(out, "watchyard-claude-2.toml was edited") {
		t.Errorf("off did not report the edited file:\n%s", out)
	}
	if b, _ := os.ReadFile(work); string(b) != edited {
		t.Errorf("edited file changed:\n%s", b)
	}
	if b, _ := os.ReadFile(theirs); string(b) != "name = \"mine\"\n" {
		t.Errorf("no-marker file changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(folder, "watchyard-claude-3.toml")); !os.IsNotExist(err) {
		t.Errorf("off kept ours: %v", err)
	}
	th.setenv("TERM_PROGRAM=WarpTerminal")
	if _, errOut, code := th.wy(t, "", "open", "claude-3"); code != 4 || !strings.Contains(errOut, "wy term warp on") {
		t.Errorf("open without a tab config: exit %d %s", code, errOut)
	}
	if after := parentSnap(t, th.root, th.wyHome); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("something was written next to the temp roots:\n%v\n%v", before, after)
	}
}

// warpDataRel is Warp data folder under the temp HOME for this OS (see warp.DataFolder).
func warpDataRel() string {
	if runtime.GOOS == "darwin" {
		return ".warp"
	}
	return ".local/share/warp-terminal"
}
