package ledger_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/ledger"
	"github.com/mraj602/switchyard/internal/setup/model"
)

const stateDir = "/home/u/.local/state/switchyard"

func termRun(id, piece, verb, root, registry string, argv ...string) model.Action {
	return model.Action{ID: id, Op: model.OpRun, Step: model.StepTerminal, Argv: append([]string{"claude", "plugin"}, argv...),
		Touches: []string{"/home/u/.claude-3/settings.json", root + "/" + registry, ledger.JournalMark(stateDir, piece, verb)},
		Watch:   []string{root}}
}

func revertAll(m model.Manifest) model.Manifest {
	for i := range m.Entries {
		m.Entries[i].Phase = model.PhaseReverted
	}
	return m
}

// The pane's `marketplace add sy-local` in a root that lacked it is recorded (Markets), its
// install root too (Installed); their reverse and their undo take them away.
func TestPaneMarketplaceRecorded(t *testing.T) {
	root := "/home/u/.claude-3/plugins"
	on := model.Plan{Actions: []model.Action{
		termRun("a1", ledger.PiecePane, ledger.VerbMarketplaceAdd, root, "known_marketplaces.json", "marketplace", "add", stateDir+"/plugin/marketplace", "--json"),
		termRun("a2", ledger.PiecePane, ledger.VerbInstall, root, "installed_plugins.json", "install", "switchyard-pane@sy-local", "--json"),
	}}
	m := manifestOf(on, model.PhaseDone)
	l := ledger.Update(baseLedger(), on, m)
	p := l.Terminal.Pane
	if p == nil || !reflect.DeepEqual(p.Markets, []string{root}) || !reflect.DeepEqual(p.Installed, []string{root}) ||
		!reflect.DeepEqual(p.Targets, []string{"/home/u/.claude-3/settings.json"}) {
		b, _ := json.Marshal(l.Terminal)
		t.Fatalf("on: %s", b)
	}
	if r := ledger.Revert(l, revertAll(manifestOf(on, model.PhaseDone))); r.Terminal != nil || !reflect.DeepEqual(r.Plugin, baseLedger().Plugin) {
		b, _ := json.Marshal(r)
		t.Fatalf("undo of on: %s", b)
	}
	off := model.Plan{Actions: []model.Action{
		termRun("b1", ledger.PiecePane, ledger.VerbUninstall, root, "installed_plugins.json", "uninstall", "switchyard-pane@sy-local", "--json"),
		termRun("b2", ledger.PiecePane, ledger.VerbMarketplaceRemove, root, "known_marketplaces.json", "marketplace", "remove", "sy-local", "--json"),
	}}
	mo := manifestOf(off, model.PhaseDone)
	gone := ledger.Update(l, off, mo)
	if gone.Terminal != nil {
		b, _ := json.Marshal(gone.Terminal)
		t.Fatalf("off: %s", b)
	}
	back := ledger.Revert(gone, revertAll(mo))
	if !reflect.DeepEqual(back.Terminal, l.Terminal) {
		b, _ := json.Marshal(back.Terminal)
		t.Fatalf("undo of off: %s", b)
	}
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// Titles on over a value the person had keeps it (TitlePrior); off puts it back with a
// json_set, which counts as off; undoing either does the right thing.
func TestTitlePrior(t *testing.T) {
	f3, f1 := "/home/u/.claude-3/settings.json", "/home/u/.claude/settings.json"
	on := model.Plan{Actions: []model.Action{
		{ID: "a1", Op: model.OpJSONSet, File: f3, Pointer: ledger.TitleEnvPointer, Value: json.RawMessage(`"1"`), Before: json.RawMessage(`"0"`)},
		{ID: "a2", Op: model.OpJSONSet, File: f1, Pointer: ledger.TitleEnvPointer, Value: json.RawMessage(`"1"`)},
	}}
	mOn := manifestOf(on, model.PhaseDone)
	for i := range mOn.Entries {
		mOn.Entries[i].Paths[0].Keys[0].AfterSHA256 = sum(`"1"`)
	}
	l := ledger.Update(baseLedger(), on, mOn)
	if tm := l.Terminal; tm == nil || len(tm.Titles.Targets) != 2 || !reflect.DeepEqual(tm.TitlePrior, map[string]string{f3: `"0"`}) {
		b, _ := json.Marshal(l.Terminal)
		t.Fatalf("on: %s", b)
	}
	if r := ledger.Revert(l, revertAll(mOn)); r.Terminal != nil {
		b, _ := json.Marshal(r.Terminal)
		t.Fatalf("undo of on: %s", b)
	}
	off := model.Plan{Actions: []model.Action{
		{ID: "b1", Op: model.OpJSONSet, File: f3, Pointer: ledger.TitleEnvPointer, Value: json.RawMessage(`"0"`), Before: json.RawMessage(`"1"`)},
		{ID: "b2", Op: model.OpJSONDelete, File: f1, Pointer: ledger.TitleEnvPointer, Before: json.RawMessage(`"1"`)},
	}}
	mOff := manifestOf(off, model.PhaseDone)
	mOff.Entries[0].Paths[0].Keys[0].AfterSHA256 = sum(`"0"`)
	gone := ledger.Update(l, off, mOff)
	if tm := gone.Terminal; tm == nil || (tm.Titles != nil && len(tm.Titles.Targets) != 0) || tm.TitlePrior[f3] != `"0"` {
		b, _ := json.Marshal(gone.Terminal)
		t.Fatalf("off: %s", b)
	}
	back := ledger.Revert(gone, revertAll(mOff))
	if tm := back.Terminal; tm == nil || tm.Titles == nil || len(tm.Titles.Targets) != 2 || tm.TitlePrior[f3] != `"0"` {
		b, _ := json.Marshal(back.Terminal)
		t.Fatalf("undo of off: %s", b)
	}
}
