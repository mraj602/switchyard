package plan

import (
	"testing"

	"github.com/mraj602/switchyard/internal/setup/model"
)

func TestMergeTerminal(t *testing.T) {
	dir := "/s/plugin/marketplace"
	env := map[string]*string{"CLAUDE_CONFIG_DIR": nil}
	add := model.Action{Step: model.StepPlugin, Op: model.OpRun, Argv: []string{"claude", "plugin", "marketplace", "add", dir, "--json"},
		Env: env, Watch: []string{"/h/.claude/plugins"}}
	base := model.Plan{
		Actions: []model.Action{
			{Step: model.StepLaunchers, Op: model.OpWriteFile, Path: "/h/.local/bin/claude-2"},
			add,
			{Step: model.StepPlugin, Op: model.OpWriteTree, Path: dir},
			{Step: model.StepConfig, Op: model.OpConfigSet, Path: "/c/config.toml", Key: "slots.labels"},
		},
		Keep: []model.Keep{{What: "link"}}, Warnings: []string{"w1"},
	}
	termAdd := add
	termAdd.Step = model.StepTerminal
	termAdd.Touches = []string{"/s/term/journal/pane.marketplace-add"}
	term := model.Plan{
		Actions: []model.Action{
			{Step: model.StepPlugin, Op: model.OpWriteTree, Path: dir},
			termAdd,
			{Step: model.StepTerminal, Op: model.OpRun, Argv: []string{"claude", "plugin", "install", PaneID, "--json"}, Env: env},
			{Step: model.StepConfig, Op: model.OpConfigSet, Path: "/c/config.toml", Key: "terminal.titles"},
		},
		Keep: []model.Keep{{What: "tab"}}, Warnings: []string{"w1", "w2"},
	}
	p := MergeTerminal(base, term)
	var got []string
	for _, a := range p.Actions {
		got = append(got, a.ID+":"+string(a.Step)+":"+string(a.Op)+":"+a.Key)
	}
	want := []string{"a1:plugin:run:", "a2:plugin:write_tree:", "a3:launchers:write_file:", "a4:terminal:run:",
		"a5:config:config_set:slots.labels", "a6:config:config_set:terminal.titles"}
	if len(got) != len(want) {
		t.Fatalf("actions %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("action %d = %s, want %s", i, got[i], want[i])
		}
	}
	if len(p.Keep) != 2 || len(p.Warnings) != 2 || p.PlanID == "" || p.Summary.Changes != 6 {
		t.Errorf("keep %v warnings %v id %q summary %+v", p.Keep, p.Warnings, p.PlanID, p.Summary)
	}
	if !WritesMarketplace(base, dir) || WritesMarketplace(model.Plan{}, dir) {
		t.Error("WritesMarketplace")
	}
	if MergeTerminal(base, term).PlanID != p.PlanID {
		t.Error("plan_id is not stable")
	}
}
