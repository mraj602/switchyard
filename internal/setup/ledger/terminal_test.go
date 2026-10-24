package ledger_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// goldenPlan reads a terminal plan golden of the planner (internal/setup/plan/testdata).
func goldenPlan(t *testing.T, name string) model.Plan {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "plan", "testdata", "terminal", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var p model.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// manifestOf journals every action of p as apply would (paths, keys, before states).
func manifestOf(p model.Plan, phase string) model.Manifest {
	var m model.Manifest
	for i, a := range p.Actions {
		e := model.Entry{Seq: i + 1, Action: a.ID, Op: a.Op, Phase: phase, Created: []string{}}
		after := &model.PathState{Type: model.TypeFile}
		switch a.Op {
		case model.OpRun:
			for _, t := range a.Touches {
				e.Paths = append(e.Paths, model.PathEntry{Path: t, Before: model.PathState{Type: model.TypeFile}, After: after})
			}
		case model.OpJSONSet, model.OpJSONDelete:
			e.Paths = []model.PathEntry{{Path: a.File, Before: model.PathState{Type: model.TypeFile}, After: after,
				Keys: []model.KeyChange{{Pointer: a.Pointer, BeforePresent: a.Op == model.OpJSONDelete}}}}
		case model.OpWriteFile:
			e.Paths = []model.PathEntry{{Path: a.Path, Before: model.PathState{Type: model.TypeAbsent}, After: after}}
		case model.OpMoveToBackup:
			e.Paths = []model.PathEntry{{Path: a.Path, Before: model.PathState{Type: model.TypeFile, SHA256: a.Pre[0].State.SHA256},
				After: &model.PathState{Type: model.TypeAbsent}}}
		case model.OpMkdir:
			e.Paths = []model.PathEntry{{Path: a.Path, Before: model.PathState{Type: model.TypeAbsent}, After: &model.PathState{Type: model.TypeDir}}}
		case model.OpConfigSet:
			e.Paths = []model.PathEntry{{Path: a.Path, Before: model.PathState{Type: model.TypeFile}, After: after}}
		}
		m.Entries = append(m.Entries, e)
	}
	return m
}

func baseLedger() model.Ledger {
	return model.Ledger{V: 1, Plugin: &model.LedgerPlugin{ID: "watchyard@wy-local", Marketplace: "/home/u/.local/state/watchyard/plugin/marketplace",
		Roots: []string{"/home/u/.claude/plugins", "/home/u/.claude-3/plugins"}, Targets: []string{"/home/u/.claude/settings.json", "/home/u/.claude-3/settings.json"}}}
}

// Every piece on records the terminal block; the undo of that run takes it all away and
// leaves the watchyard plugin's record alone.
func TestTerminalUpdateAndRevert(t *testing.T) {
	p := goldenPlan(t, "all-on")
	m := manifestOf(p, model.PhaseDone)
	prev := baseLedger()
	l := ledger.Update(prev, p, m)
	tm := l.Terminal
	if tm == nil || tm.Pane == nil || len(tm.Pane.Targets) != 2 || tm.WarpPlugin == nil || len(tm.WarpPlugin.Roots) != 2 ||
		tm.WarpPlugin.Ref != ledger.WarpRef || len(tm.WarpPlugin.Targets) != 2 || tm.Titles == nil || len(tm.Titles.Targets) != 2 ||
		len(tm.Tabs) != 3 || tm.Warp == nil || tm.Warp.Channel != "stable" || tm.Warp.Folder != "/home/u/.local/share/warp-terminal/tab_configs" {
		b, _ := json.MarshalIndent(l, "", " ")
		t.Fatalf("terminal block:\n%s", b)
	}
	if tm.Tabs[1].Label != "work" || tm.Tabs[1].SHA256 != p.Actions[10].ContentSHA256 {
		t.Errorf("tab %+v", tm.Tabs[1])
	}
	if !reflect.DeepEqual(l.Plugin, prev.Plugin) {
		t.Errorf("plugin record changed: %+v", l.Plugin)
	}
	if again := ledger.Update(l, p, m); !reflect.DeepEqual(again, l) {
		t.Errorf("not idempotent")
	}
	for i := range m.Entries {
		m.Entries[i].Phase = model.PhaseReverted
	}
	r := ledger.Revert(l, m)
	if r.Terminal != nil || !reflect.DeepEqual(r.Plugin, prev.Plugin) {
		b, _ := json.MarshalIndent(r, "", " ")
		t.Fatalf("revert:\n%s", b)
	}
}

// `off` empties the block; undoing the `off` gives it back.
func TestTerminalOffAndUndo(t *testing.T) {
	on := goldenPlan(t, "all-on")
	l := ledger.Update(baseLedger(), on, manifestOf(on, model.PhaseDone))
	off := goldenPlan(t, "all-off")
	// The golden's tab hashes are the planner's renders, the same as the on plan's.
	m := manifestOf(off, model.PhaseDone)
	gone := ledger.Update(l, off, m)
	if gone.Terminal != nil || gone.Plugin == nil {
		b, _ := json.MarshalIndent(gone.Terminal, "", " ")
		t.Fatalf("after off:\n%s", b)
	}
	for i := range m.Entries {
		m.Entries[i].Phase = model.PhaseReverted
	}
	back := ledger.Revert(gone, m)
	bt := back.Terminal
	if bt == nil || bt.Pane == nil || bt.WarpPlugin == nil || bt.Titles == nil || len(bt.Tabs) != 3 || len(bt.Titles.Targets) != 2 {
		b, _ := json.MarshalIndent(back.Terminal, "", " ")
		t.Fatalf("undo of off:\n%s", b)
	}
	for i, tab := range bt.Tabs {
		if tab.SHA256 == "" || tab.Path == "" {
			t.Errorf("tab %d %+v", i, tab)
		}
	}
}

// A run without a journal mark is not terminal: the generic code handles it as before.
func TestJournalMark(t *testing.T) {
	if got := ledger.JournalMark("/s", ledger.PiecePane, ledger.VerbInstall); got != filepath.Join("/s", "term", "journal", "pane.install") {
		t.Fatal(got)
	}
	p := model.Plan{Actions: []model.Action{{ID: "a1", Op: model.OpRun, Step: model.StepPlugin,
		Argv:    []string{"claude", "plugin", "install", "watchyard@wy-local"},
		Touches: []string{"/h/.claude/settings.json", "/h/.claude/plugins/installed_plugins.json"}}}}
	m := manifestOf(p, model.PhaseDone)
	l := ledger.Update(model.Ledger{}, p, m)
	if l.Terminal != nil || l.Plugin == nil {
		t.Fatalf("%+v", l)
	}
}

// The watchyard plugin's refresh (`wy term titles on` with a stale wy-local) changes no
// piece, and its undo leaves the watchyard plugin's record alone.
func TestHooksRefreshRecordsNothing(t *testing.T) {
	root := "/home/u/.claude/plugins"
	p := model.Plan{Actions: []model.Action{
		{ID: "a1", Op: model.OpRun, Step: model.StepTerminal, Argv: []string{"claude", "plugin", "marketplace", "update", "wy-local", "--json"},
			Touches: []string{"/home/u/.claude/settings.json", root + "/known_marketplaces.json",
				ledger.JournalMark("/home/u/.local/state/watchyard", ledger.PieceHooks, ledger.VerbMarketplaceUpdate)}, Watch: []string{root}},
		{ID: "a2", Op: model.OpRun, Step: model.StepTerminal, Argv: []string{"claude", "plugin", "update", "watchyard@wy-local", "--json"},
			Touches: []string{"/home/u/.claude/settings.json", root + "/installed_plugins.json",
				ledger.JournalMark("/home/u/.local/state/watchyard", ledger.PieceHooks, ledger.VerbUpdate)}, Watch: []string{root}},
	}}
	m := manifestOf(p, model.PhaseDone)
	prev := baseLedger()
	l := ledger.Update(prev, p, m)
	if l.Terminal != nil || !reflect.DeepEqual(l.Plugin, prev.Plugin) {
		b, _ := json.MarshalIndent(l, "", " ")
		t.Fatalf("update:\n%s", b)
	}
	for i := range m.Entries {
		m.Entries[i].Phase = model.PhaseReverted
	}
	if r := ledger.Revert(l, m); r.Terminal != nil || !reflect.DeepEqual(r.Plugin, prev.Plugin) {
		b, _ := json.MarshalIndent(r, "", " ")
		t.Fatalf("revert:\n%s", b)
	}
}
