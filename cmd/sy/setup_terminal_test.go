package main

// `sy setup --terminal …` (docs/phases/7.md WP-I): the terminal pieces join the setup plan,
// one backup set, one undo; `sy term titles on` with a stale sy-local updates the switchyard
// plugin so the title hooks run.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

func TestSetupTerminalFlag(t *testing.T) {
	th := newTermHomeWith(t, detecttest.Laptop(t))
	tree0 := testtree.Take(t, th.root, snapOpts)
	if _, _, code := th.sy(t, "", "setup", "--terminal", "tabs"); code != 2 {
		t.Errorf("--terminal tabs: exit %d, want 2", code)
	}
	out := th.mustSy(t, "setup", "--yes", "--hub", "later", "--terminal", "titles,pane")
	b, _ := os.ReadFile(th.h.Abs(".claude/settings.json"))
	if !strings.Contains(string(b), `"CLAUDE_CODE_DISABLE_TERMINAL_TITLE": "1"`) {
		t.Fatalf("settings:\n%s\n%s", b, out)
	}
	lt := th.ledgerTerm(t)
	if lt["titles"] == nil || lt["pane"] == nil {
		t.Fatalf("ledger terminal %v", lt)
	}
	joined := strings.Join(th.runs(t), "\n")
	if !strings.Contains(joined, "plugin install switchyard-pane@sy-local") ||
		strings.Count(joined, "plugin marketplace add") > strings.Count(joined, "plugin install switchyard@sy-local") {
		t.Errorf("runs:\n%s", joined)
	}
	sets, _ := os.ReadDir(filepath.Join(th.state(), "backup"))
	if len(sets) != 1 {
		t.Errorf("%d backup sets, want 1", len(sets))
	}
	th.mustSy(t, "setup", "--undo", "--yes")
	testtree.Equal(t, tree0, testtree.Take(t, th.root, snapOpts))
}

func TestTermTitlesRefreshesHooks(t *testing.T) {
	th := newTermHome(t, detecttest.Laptop(t))
	// A sy-local written by an older sy: titles on rewrites it and updates the plugin.
	mkt := filepath.Join(th.state(), "plugin", "marketplace")
	writeFile(t, filepath.Join(mkt, "stale.txt"), "old")
	_ = os.Remove(th.wrapLog)
	th.mustSy(t, "term", "titles", "on", "--yes")
	joined := strings.Join(th.runs(t), "\n")
	for _, w := range []string{"plugin marketplace update sy-local --json", "plugin update switchyard@sy-local --json"} {
		if !strings.Contains(joined, w) {
			t.Errorf("not run %q:\n%s", w, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(mkt, "stale.txt")); err == nil {
		t.Error("the marketplace was not rewritten")
	}
	if lt := th.ledgerTerm(t); lt["titles"] == nil {
		t.Fatalf("ledger %v", lt)
	}
	th.mustSy(t, "setup", "--undo", "--yes")
	if lt := th.ledgerTerm(t); lt != nil {
		t.Errorf("ledger after undo: %v", lt)
	}
	if led, err := ledger.Read(th.state()); err != nil || led.Plugin == nil {
		t.Errorf("the switchyard plugin's record is gone after the undo: %v %+v", err, led)
	}
}

// `sy term setup --pane` refuses a Claude Code older than 2.1.296 (exit 4), and changes
// nothing.
func TestTermPaneRefusesOldClaude(t *testing.T) {
	th := newTermHome(t, detecttest.Laptop(t))
	wrap := filepath.Join(th.syHome, "wrapbin", "claude")
	b, err := os.ReadFile(wrap)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(b), "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '2.1.295 (Claude Code)'; exit 0; fi\n", 1)
	writeExec(t, wrap, old)
	tree0 := testtree.Take(t, th.root, snapOpts)
	_, errOut, code := th.sy(t, "", "term", "setup", "--pane", "--yes")
	if code != 4 || !strings.Contains(errOut, "2.1.296") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	testtree.Equal(t, tree0, testtree.Take(t, th.root, snapOpts))
	if _, _, code := th.sy(t, "", "setup", "--yes", "--hub", "later", "--terminal", "pane"); code != 4 {
		t.Errorf("setup --terminal pane: exit %d, want 4", code)
	}
}

// sy uninstall takes every terminal piece away first (the pane before sy-local goes) and
// leaves no ledger terminal block; its undo gives them back.
func TestUninstallRemovesTerminalPieces(t *testing.T) {
	h := detecttest.Fresh(t)
	h.B.File(".claude-2/.claude.json", `{}`, 0o600)
	th := newTermHome(t, h)
	folder := filepath.Join(h.Abs(warpDataRel()), "tab_configs")
	if err := os.MkdirAll(filepath.Dir(folder), 0o755); err != nil {
		t.Fatal(err)
	}
	th.mustSy(t, "term", "setup", "--pane", "--warp-plugin", "--titles", "--warp-tabs", "--yes")
	if lt := th.ledgerTerm(t); lt["pane"] == nil || lt["warp_plugin"] == nil || lt["titles"] == nil || lt["tabs"] == nil {
		t.Fatalf("ledger %v", lt)
	}
	on := testtree.Take(t, th.root, snapOpts)
	_ = os.Remove(th.wrapLog)
	out := th.mustSy(t, "uninstall", "--yes")
	runs := th.runs(t)
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
	if pane < 0 || mkt < 0 || pane > mkt || !strings.Contains(joined, "plugin uninstall warp@claude-code-warp") {
		t.Errorf("runs:\n%s\n%s", joined, out)
	}
	if lt := th.ledgerTerm(t); lt != nil {
		t.Errorf("ledger terminal after uninstall: %v", lt)
	}
	b, _ := os.ReadFile(h.Abs(".claude/settings.json"))
	if strings.Contains(string(b), "CLAUDE_CODE_DISABLE_TERMINAL_TITLE") {
		t.Errorf("titles key left:\n%s", b)
	}
	if ents, _ := os.ReadDir(folder); len(ents) != 0 {
		t.Errorf("tab configs left: %v", ents)
	}
	th.mustSy(t, "setup", "--undo", "--yes")
	testtree.Equal(t, on, testtree.Take(t, th.root, snapOpts))
	if lt := th.ledgerTerm(t); lt["pane"] == nil || lt["titles"] == nil {
		t.Errorf("ledger after undo: %v", lt)
	}
}
