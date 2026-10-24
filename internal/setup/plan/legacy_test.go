package plan_test

import (
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/brand"
	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/plan"
	"github.com/mraj602/switchyard/internal/warp"
)

// rename.md 2.4: a folder whose only plugin is the one of the old name gets it disabled and
// the current one installed, as if it had none.
func TestLegacyPluginIsReplaced(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	h := detecttest.Laptop(t)
	st := h.State()
	st.SettingsTargets[0].EnabledPlugins["oldname@sy-local"] = true
	st.SettingsTargets[0].EnabledPlugins["oldname-pane@sy-local"] = true
	st.SettingsTargets[0].LegacyPlugins = []string{"oldname-pane@sy-local", "oldname@skills-dir", "oldname@sy-local"}
	st.PluginRoots[0].Markets = append(st.PluginRoots[0].Markets, "sy-local")
	p := mustPlan(t, st, model.Choices{NoStatusLine: true, Share: []string{"defaults"}})
	var runs []string
	for _, a := range p.Actions {
		if a.Op == model.OpRun {
			runs = append(runs, strings.Join(a.Argv, " "))
		}
	}
	got := strings.Join(runs, "\n")
	for _, want := range []string{
		"claude plugin disable oldname-pane@sy-local --json\nclaude plugin disable oldname@sy-local --json\n",
		"claude plugin install " + plan.PluginID + " --json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("runs lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "oldname@skills-dir") {
		t.Error("a skills-dir plugin cannot be disabled")
	}
	w := strings.Join(p.Warnings, "\n")
	if !strings.Contains(w, "oldname@skills-dir") || !strings.Contains(w, "oldname@sy-local from before the rename") {
		t.Errorf("warnings:\n%s", w)
	}
	// --no-plugin leaves them alone.
	p = mustPlan(t, st, model.Choices{NoStatusLine: true, NoPlugin: true})
	for _, a := range p.Actions {
		if a.Op == model.OpRun && strings.Contains(strings.Join(a.Argv, " "), "oldname") {
			t.Errorf("--no-plugin disabled %v", a.Argv)
		}
	}
}

// rename.md 2.8: a tab config sy wrote under the old prefix moves away once the current one
// is written; one edited since is the person's and stays.
func TestLegacyTabsMoveAway(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	st, f := onState()
	folder := f.Warp.Folder
	kept := model.LedgerTab{Path: folder + "/oldname-work.toml", SHA256: "edited-old", Label: "work"}
	moved := model.LedgerTab{Path: folder + "/oldname-home.toml", SHA256: "old", Label: "home"}
	st.Ledger.Terminal.Tabs = append(st.Ledger.Terminal.Tabs, kept, moved)
	f.Tabs = append(f.Tabs,
		warp.Found{Path: kept.Path, Label: "work", SHA256: "changed", Marker: true},
		warp.Found{Path: moved.Path, Label: "home", SHA256: "old", Marker: true})
	p := termPlan(t, st, model.TermChoices{WarpTabs: true}, f)
	if len(p.Actions) != 1 || p.Actions[0].Op != model.OpMoveToBackup || p.Actions[0].Path != moved.Path {
		t.Fatalf("legacy tabs: %s", dump(p))
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "oldname-work.toml (from before the rename) was edited") {
		t.Errorf("edited legacy tab not reported: %v", p.Warnings)
	}
}

// rename.md 2.4: after the command's rename, the marketplace of its old name is removed before
// the current one is added.
func TestLegacyMarketplaceIsRemoved(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	defer brand.SetLegacyCLIForTest("oldcli")()
	h := detecttest.Laptop(t)
	st := h.State()
	st.SettingsTargets[0].EnabledPlugins["oldname@oldcli-local"] = true
	st.SettingsTargets[0].LegacyPlugins = []string{"oldname@oldcli-local"}
	st.PluginRoots[0].Markets = append(st.PluginRoots[0].Markets, "oldcli-local")
	p := mustPlan(t, st, model.Choices{NoStatusLine: true, Share: []string{"defaults"}})
	var runs []string
	for _, a := range p.Actions {
		if a.Op == model.OpRun {
			runs = append(runs, strings.Join(a.Argv, " "))
		}
	}
	got := strings.Join(runs, "\n")
	want := "claude plugin marketplace remove oldcli-local --json\nclaude plugin marketplace add "
	if !strings.Contains(got, want) {
		t.Errorf("runs lack %q:\n%s", want, got)
	}
}
