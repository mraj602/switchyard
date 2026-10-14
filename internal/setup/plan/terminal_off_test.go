package plan_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/plan"
)

func runsOf(p model.Plan) []string {
	var out []string
	for _, a := range p.Actions {
		if a.Op == model.OpRun {
			// The fake home is POSIX-shaped; plan joins with the host separator.
			out = append(out, filepath.ToSlash(strings.Join(a.Argv[2:len(a.Argv)-1], " ")+"@"+a.Touches[0]))
		}
	}
	return out
}

// `off` takes back only what the ledger says sy did: a copy of Warp's plugin (and its
// marketplace) the person installed in ~/.claude, and a pane they enabled there, stay.
func TestTerminalOffOnlyRecordedRoots(t *testing.T) {
	st, f := onState()
	lt := st.Ledger.Terminal
	lt.WarpPlugin.Roots = []string{"/home/u/.claude-3/plugins"}
	lt.WarpPlugin.Installed = []string{"/home/u/.claude-3/plugins"}
	lt.WarpPlugin.Targets = []string{"/home/u/.claude-3/settings.json"}
	lt.Pane.Installed = []string{"/home/u/.claude-3/plugins"}
	lt.Pane.Targets = []string{"/home/u/.claude-3/settings.json"}
	p := termPlan(t, st, model.TermChoices{Pane: true, WarpPlugin: true, Off: true}, f)
	got := strings.Join(runsOf(p), "\n")
	want := strings.Join([]string{
		"uninstall switchyard-pane@sy-local@/home/u/.claude-3/settings.json",
		"uninstall warp@claude-code-warp@/home/u/.claude-3/settings.json",
		"marketplace remove claude-code-warp@/home/u/.claude-3/settings.json",
	}, "\n")
	if got != want {
		t.Fatalf("off runs:\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(dump(p), "/home/u/.claude/settings.json") {
		t.Errorf("off touched ~/.claude: %s", dump(p))
	}

	// sy only enabled Warp's plugin in ~/.claude (the person installed it and added the
	// marketplace): off disables it there and keeps the install and the marketplace.
	st, f = onState()
	lt = st.Ledger.Terminal
	lt.WarpPlugin.Roots, lt.WarpPlugin.Installed = nil, nil
	lt.WarpPlugin.Targets = []string{"/home/u/.claude/settings.json"}
	p = termPlan(t, st, model.TermChoices{WarpPlugin: true, Off: true}, f)
	if got := strings.Join(runsOf(p), "\n"); got != "disable warp@claude-code-warp@/home/u/.claude/settings.json" {
		t.Fatalf("enable-only off:\n%s", got)
	}

	// The marketplace is ours but the plugin in that root is not: both stay, with a warning.
	st, f = onState()
	lt = st.Ledger.Terminal
	lt.WarpPlugin.Installed = []string{"/home/u/.claude-3/plugins"}
	lt.WarpPlugin.Targets = []string{"/home/u/.claude-3/settings.json"}
	p = termPlan(t, st, model.TermChoices{WarpPlugin: true, Off: true}, f)
	if got := runsOf(p); len(got) != 2 || !strings.Contains(strings.Join(p.Warnings, "\n"), "installed but not by sy") {
		t.Fatalf("theirs in ~/.claude: %v %v", got, p.Warnings)
	}
}

// The pane adds sy-local to a root that lacks it; off removes it there, unless the switchyard
// plugin is (now) in that root.
func TestTerminalPaneMarketplace(t *testing.T) {
	st, f := termState(), termFacts()
	r := &st.PluginRoots[1]
	r.Plugins, r.Markets = nil, nil
	p := termPlan(t, st, model.TermChoices{Pane: true}, f)
	got := strings.Join(runsOf(p), "\n")
	if !strings.Contains(got, "marketplace add "+st.StateDir+"/plugin/marketplace@/home/u/.claude-3/settings.json") {
		t.Fatalf("on: %s", got)
	}

	st, f = onState()
	r = &st.PluginRoots[1]
	r.Plugins = []string{plan.PaneID}
	st.Ledger.Terminal.Pane.Markets = []string{"/home/u/.claude-3/plugins"}
	p = termPlan(t, st, model.TermChoices{Pane: true, Off: true}, f)
	if got := strings.Join(runsOf(p), "\n"); !strings.HasSuffix(got, "uninstall switchyard-pane@sy-local@/home/u/.claude-3/settings.json\nmarketplace remove sy-local@/home/u/.claude-3/settings.json") {
		t.Fatalf("off:\n%s", got)
	}
	// With the switchyard plugin there, sy-local stays and the record is let go.
	r.Plugins = []string{plan.PaneID, plan.PluginID}
	p = termPlan(t, st, model.TermChoices{Pane: true, Off: true}, f)
	if strings.Contains(strings.Join(runsOf(p), "\n"), "marketplace remove") || !hasKeep(p, plan.KeepForSwitch) {
		t.Fatalf("off with switchyard: %s", dump(p))
	}
}

// Titles off puts back the value the person had before titles on.
func TestTerminalTitlesOffRestoresPrior(t *testing.T) {
	st, f := onState()
	st.Ledger.Terminal.TitlePrior = map[string]string{"/home/u/.claude-3/settings.json": `"0"`}
	p := termPlan(t, st, model.TermChoices{Titles: true, Off: true}, f)
	var set, del []string
	for _, a := range p.Actions {
		switch a.Op {
		case model.OpJSONSet:
			set = append(set, a.File+"="+string(a.Value))
		case model.OpJSONDelete:
			del = append(del, a.File)
		}
	}
	if strings.Join(set, ",") != `/home/u/.claude-3/settings.json="0"` || strings.Join(del, ",") != "/home/u/.claude/settings.json" {
		t.Fatalf("titles off: set %v delete %v", set, del)
	}
	// On records the value it replaces.
	st, f = termState(), termFacts()
	f.TitleEnv["/home/u/.claude-3/settings.json"] = `"0"`
	p = termPlan(t, st, model.TermChoices{Titles: true}, f)
	for _, a := range p.Actions {
		if a.Op == model.OpJSONSet && a.File == "/home/u/.claude-3/settings.json" && string(a.Before) != `"0"` {
			t.Errorf("on before = %s", a.Before)
		}
	}
}

// `sy term off` without claude: titles and tabs go, the plugin pieces stay with a warning.
func TestTerminalOffWithoutClaude(t *testing.T) {
	st, f := onState()
	st.Claude.Path = ""
	p, err := plan.TerminalWith(st, model.TermChoices{Pane: true, WarpPlugin: true, Titles: true, WarpTabs: true, Off: true}, f, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(runsOf(p)) != 0 {
		t.Errorf("runs without claude: %v", runsOf(p))
	}
	var del, tabs int
	for _, a := range p.Actions {
		switch a.Op {
		case model.OpJSONDelete:
			del++
		case model.OpMoveToBackup:
			tabs++
		}
	}
	w := strings.Join(p.Warnings, "\n")
	if del != 2 || tabs != 3 || !strings.Contains(w, plan.PaneID) || !strings.Contains(w, plan.WarpPluginID) {
		t.Fatalf("off without claude: %d deletes, %d tabs, warnings %v", del, tabs, p.Warnings)
	}
}
