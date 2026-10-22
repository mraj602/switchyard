package main

// Regression tests of `sy term` (terminal.md T10): the Warp plugin commit is never reset
// silently, the pane's own sy-local is undone, titles off keeps the person's value, and
// `sy term off` without claude still removes titles and tab configs.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

func newFreshTermHome(t *testing.T) *termHome {
	t.Helper()
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	return newTermHome(t, h)
}

// claude runs the wrapper claude (and so fakeclaude) as the person would, as ~/.claude.
func (th *termHome) claude(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(th.syHome, "wrapbin", "claude"), args...)
	cmd.Env = th.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claude %v: %v\n%s", args, err, out)
	}
}

func (th *termHome) editLedger(t *testing.T, edit func(map[string]any)) {
	t.Helper()
	p := ledger.Path(th.state())
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var l map[string]any
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	edit(l)
	b, _ = json.MarshalIndent(l, "", "  ")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func warpCommitOf(t *testing.T, th *termHome) string {
	t.Helper()
	wp, _ := th.ledgerTerm(t)["warp_plugin"].(map[string]any)
	c, _ := wp["commit"].(string)
	return c
}

// The commit is recorded when sy adds Warp's marketplace; a re-run with nothing to do, or
// another piece's run, leaves it (so sy doctor keeps warning); --accept-update takes the
// new one.
func TestTermWarpCommitDrift(t *testing.T) {
	th := newFreshTermHome(t)
	git := filepath.Join(th.syHome, "warp-src", ".git")
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	writeFile(t, filepath.Join(git, "HEAD"), a+"\n")
	th.mustSy(t, "term", "setup", "--warp-plugin", "--yes")
	if c := warpCommitOf(t, th); c != a {
		t.Fatalf("commit after on: %q", c)
	}
	writeFile(t, filepath.Join(git, "HEAD"), b+"\n")
	if out := th.mustSy(t, "term", "setup", "--warp-plugin", "--yes"); !strings.Contains(out, "0 changes") {
		t.Fatalf("second on:\n%s", out)
	}
	th.mustSy(t, "term", "titles", "on", "--yes")
	if c := warpCommitOf(t, th); c != a {
		t.Fatalf("the recorded commit moved without --accept-update: %q", c)
	}
	if out := th.mustSy(t, "term", "setup", "--accept-update", "--yes"); !strings.Contains(out, "Accepted Warp's plugin checkout at "+b) {
		t.Errorf("accept:\n%s", out)
	}
	if c := warpCommitOf(t, th); c != b {
		t.Fatalf("commit after --accept-update: %q", c)
	}
}

// The pane adds sy-local to a root that lacks it: undo, `sy term off` and `sy uninstall`
// each leave that root's plugin registry as it was.
func TestTermPaneOwnMarketplace(t *testing.T) {
	th := newFreshTermHome(t)
	// The person took the switchyard plugin and sy-local out of ~/.claude's root; sy's
	// ledger no longer has the plugin.
	th.claude(t, "plugin", "uninstall", "switchyard@sy-local", "--json")
	th.claude(t, "plugin", "marketplace", "remove", "sy-local", "--json")
	th.editLedger(t, func(l map[string]any) { delete(l, "plugin") })
	plugins := th.h.Abs(".claude/plugins")
	known := filepath.Join(plugins, "known_marketplaces.json")
	tree0 := testtree.Take(t, th.root, snapOpts)
	// Claude Code keeps an uninstalled plugin's cache (marked orphaned) and rewrites its
	// registries: compare what they say, not the folder.
	registries := func() string {
		t.Helper()
		var out []string
		for _, f := range []string{"known_marketplaces.json", "installed_plugins.json"} {
			b, _ := os.ReadFile(filepath.Join(plugins, f))
			var v any
			_ = json.Unmarshal(b, &v)
			c, _ := json.Marshal(v)
			out = append(out, f+": "+string(c))
		}
		return strings.Join(out, "\n")
	}
	reg0 := registries()
	sameRegistries := func(what string) {
		t.Helper()
		if got := registries(); got != reg0 {
			t.Errorf("%s: registries\n%s\nwant\n%s", what, got, reg0)
		}
	}

	on := func() {
		t.Helper()
		_ = os.Remove(th.wrapLog)
		th.mustSy(t, "term", "setup", "--pane", "--yes")
		if runs := strings.Join(th.runs(t), "\n"); !strings.Contains(runs, "plugin marketplace add ") {
			t.Fatalf("no marketplace add:\n%s", runs)
		}
		if b, _ := os.ReadFile(known); !strings.Contains(string(b), "sy-local") {
			t.Fatalf("sy-local not added:\n%s", b)
		}
		pane, _ := th.ledgerTerm(t)["pane"].(map[string]any)
		if m, _ := pane["markets"].([]any); len(m) != 1 || m[0] != plugins {
			t.Fatalf("pane record: %v", pane)
		}
	}
	gone := func(what string) {
		t.Helper()
		if b, _ := os.ReadFile(known); strings.Contains(string(b), "sy-local") {
			t.Errorf("%s: known_marketplaces.json still names sy-local:\n%s", what, b)
		}
	}

	on()
	th.mustSy(t, "setup", "--undo", "--yes")
	testtree.Equal(t, tree0, testtree.Take(t, th.root, snapOpts))

	on()
	_ = os.Remove(th.wrapLog)
	th.mustSy(t, "term", "off", "--yes")
	if runs := strings.Join(th.runs(t), "\n"); !strings.Contains(runs, "plugin marketplace remove sy-local") {
		t.Errorf("off runs:\n%s", runs)
	}
	gone("off")
	sameRegistries("off")
	if lt := th.ledgerTerm(t); lt != nil {
		t.Errorf("ledger after off: %v", lt)
	}

	on()
	th.mustSy(t, "uninstall", "--yes")
	gone("uninstall")
	sameRegistries("uninstall")
}

// Titles on over a value the person set keeps it; off puts it back, and undo of the off
// brings sy's "1" back.
func TestTermTitlesOffRestoresValue(t *testing.T) {
	th := newFreshTermHome(t)
	sp := th.h.Abs(".claude/settings.json")
	b, err := os.ReadFile(sp)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	env, _ := s["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	env[ledger.TitleEnvKey] = "0"
	s["env"] = env
	b, _ = json.MarshalIndent(s, "", "  ")
	writeFile(t, sp, string(b)+"\n")
	value := func() any {
		t.Helper()
		b, _ := os.ReadFile(sp)
		var s struct {
			Env map[string]any `json:"env"`
		}
		_ = json.Unmarshal(b, &s)
		return s.Env[ledger.TitleEnvKey]
	}
	th.mustSy(t, "term", "titles", "on", "--yes")
	if v := value(); v != "1" {
		t.Fatalf("on: %v", v)
	}
	off := th.mustSy(t, "term", "titles", "off", "--yes")
	if v := value(); v != "0" {
		t.Fatalf("off left %v, want the person's \"0\":\n%s", v, off)
	}
	th.mustSy(t, "setup", "--undo", "--yes")
	if v := value(); v != "1" {
		t.Fatalf("undo of off: %v", v)
	}
	th.mustSy(t, "term", "titles", "off", "--yes")
	if v := value(); v != "0" {
		t.Fatalf("off after the undo: %v", v)
	}
}

// Without claude, `sy term off` removes titles and tab configs and keeps the pane's record
// with a warning (as sy uninstall does).
func TestTermOffWithoutClaude(t *testing.T) {
	th := newFreshTermHome(t)
	th.mustSy(t, "term", "setup", "--pane", "--titles", "--yes")
	th.setenv("PATH=/usr/bin:/bin")
	if err := os.Remove(th.h.Abs(".local/bin/claude")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out, errOut, code := th.sy(t, "", "term", "off", "--yes")
	if code != 0 {
		t.Fatalf("off without claude: exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "needs Claude Code") {
		t.Errorf("no warning:\n%s", out)
	}
	if b, _ := os.ReadFile(th.h.Abs(".claude/settings.json")); strings.Contains(string(b), ledger.TitleEnvKey) {
		t.Errorf("titles key left:\n%s", b)
	}
	lt := th.ledgerTerm(t)
	if lt["pane"] == nil || lt["titles"] != nil {
		t.Errorf("ledger after off: %v", lt)
	}
}
